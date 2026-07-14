#!/bin/bash
# E2E test for oci-delta with real bootc images
# Supports both VM (with KVM) and privileged container modes

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
OCI_DELTA="$PROJECT_DIR/oci-delta"

# Configuration (override with env vars)
BASE_IMAGE="${BASE_IMAGE:-quay.io/fedora/fedora-bootc:43}"
TARGET_IMAGE="${TARGET_IMAGE:-quay.io/fedora/fedora-bootc:44}"
TEST_MODE="${TEST_MODE:-auto}"  # auto, vm, container
VM_USER="${VM_USER:-fedora}"
VM_PASSWORD="${VM_PASSWORD:-admin}"

workdir=$(mktemp -d "${TMPDIR:-/tmp}/test-oci-delta-XXXXXX")
VM_NAME=""
CTR_NAME=""

cleanup() {
    if [ -n "$CTR_NAME" ]; then
        podman rm -f "$CTR_NAME" 2>/dev/null || true
    fi
    if [ -n "$VM_NAME" ]; then
        cleanup_vm "$VM_NAME"
    fi
    if [ -d "$workdir" ]; then
        sudo chown -R "$USER:$USER" "$workdir" 2>/dev/null || true
        rm -rf "$workdir"
    fi
}
trap cleanup EXIT

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log() { echo -e "${GREEN}[E2E]${NC} $*" >&2; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*" >&2; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }
die() { error "$*"; exit 1; }

# Check prerequisites
check_prereqs() {
    if [ ! -x "$OCI_DELTA" ]; then
        die "oci-delta binary not found at $OCI_DELTA (run 'make build' first)"
    fi

    for cmd in podman jq skopeo; do
        if ! command -v "$cmd" &>/dev/null; then
            die "$cmd is required but not installed"
        fi
    done
}

# Detect test mode
detect_mode() {
    if [ "$TEST_MODE" != "auto" ]; then
        echo "$TEST_MODE"
        return
    fi

    if [ -e /dev/kvm ] && command -v virsh &>/dev/null; then
        echo "vm"
    else
        echo "container"
    fi
}

# VM-based E2E test (full workflow with reboot)
test_vm() {
    log "Running VM-based E2E test (full workflow with reboot verification)"

    VM_NAME="oci-delta-e2e-$$"

    log "Work directory: $workdir"

    # Use static config file from tests directory
    local config_file="$SCRIPT_DIR/e2e-config.toml"
    if [ ! -f "$config_file" ]; then
        die "Config file not found: $config_file"
    fi

    # Pull base image for bootc-image-builder (newer versions no longer pull automatically)
    log "Pulling base image: $BASE_IMAGE"
    sudo podman pull "$BASE_IMAGE" || die "Failed to pull base image"

    # Build bootc VM disk using bootc-image-builder
    log "Building bootc VM disk with bootc-image-builder"
    build_bootc_disk "$BASE_IMAGE" "$workdir" || die "Disk build failed"

    # Create and start VM
    log "Creating VM: $VM_NAME"
    create_vm "$VM_NAME" "$workdir/qcow2/disk.qcow2" || die "VM creation failed"

    # Wait for VM to boot and get IP
    log "Waiting for VM to boot..."
    local vm_ip
    vm_ip=$(wait_for_vm_ip "$VM_NAME") || die "VM failed to boot or get IP"
    log "VM IP: $vm_ip"

    # Wait for SSH to be ready (bootc VMs can take a while to boot)
    wait_for_ssh "$vm_ip" 180 || die "SSH not available after 3 minutes"

    # Get initial bootc status
    log "Verifying initial bootc deployment"
    ssh_exec "$vm_ip" "sudo bootc status --json" > "$workdir/status-before.json"
    local initial_image=$(jq -r '.status.booted.image.image.image' "$workdir/status-before.json" 2>/dev/null || echo "unknown")
    log "Initial booted image: $initial_image"

    # Copy oci-delta binary to VM
    log "Copying oci-delta binary to VM"
    scp_to_vm "$vm_ip" "$OCI_DELTA" "/var/home/$VM_USER/oci-delta"
    ssh_exec "$vm_ip" "chmod +x /var/home/$VM_USER/oci-delta"

    # Install podman in VM if needed
    log "Ensuring podman is available in VM"
    ssh_exec "$vm_ip" "command -v podman || sudo dnf install -y podman" || warn "Could not verify podman"

    # Pull and export images IN THE VM
    log "Pulling images in VM: $BASE_IMAGE (this may take a few minutes)..."
    ssh_exec_verbose "$vm_ip" "podman pull $BASE_IMAGE" || die "Failed to pull base image in VM"

    log "Pulling images in VM: $TARGET_IMAGE (this may take a few minutes)..."
    ssh_exec_verbose "$vm_ip" "podman pull $TARGET_IMAGE" || die "Failed to pull target image in VM"

    log "Exporting base image to OCI archive..."
    ssh_exec_verbose "$vm_ip" "podman save --format oci-archive -o /var/home/$VM_USER/base.oci-archive $BASE_IMAGE" \
        || die "Failed to export base image in VM"

    log "Exporting target image to OCI archive..."
    ssh_exec_verbose "$vm_ip" "podman save --format oci-archive -o /var/home/$VM_USER/target.oci-archive $TARGET_IMAGE" \
        || die "Failed to export target image in VM"

    # Create delta IN THE VM (can take several minutes for large bootc images)
    log "Creating delta in VM (this may take 3-5 minutes for large images)..."
    ssh_exec_verbose "$vm_ip" "cd /var/home/$VM_USER && ./oci-delta create --debug base.oci-archive target.oci-archive update.oci-delta" \
        || die "Delta creation failed"

    # Get size comparison from VM
    log "Checking delta size"
    local size_info=$(ssh_exec "$vm_ip" "ls -lh /var/home/$VM_USER/*.oci-archive /var/home/$VM_USER/update.oci-delta | awk '{print \$5, \$9}'")
    log "File sizes in VM:"
    echo "$size_info" | while read line; do log "  $line"; done

    # Apply delta in VM (use container-storage since we pulled the exact base image there)
    log "Applying delta in VM (may take 1-2 minutes)..."
    ssh_exec_verbose "$vm_ip" "cd /var/home/$VM_USER && ./oci-delta apply --debug --container-storage ~/.local/share/containers/storage update.oci-delta reconstructed.oci-archive" \
        || die "Delta apply failed"

    # Verify applied image
    log "Verifying applied image integrity"
    ssh_exec "$vm_ip" "skopeo inspect --raw oci-archive:/var/home/$VM_USER/reconstructed.oci-archive > /dev/null" \
        || die "Applied image is not a valid OCI archive"

    # Switch to new image
    log "Switching to new bootc image (may take 1-2 minutes)..."
    ssh_exec_verbose "$vm_ip" "sudo bootc switch --transport=oci-archive /var/home/$VM_USER/reconstructed.oci-archive" \
        || die "bootc switch failed"

    # Verify staged deployment
    log "Verifying staged deployment"
    ssh_exec "$vm_ip" "sudo bootc status --json" > "$workdir/status-staged.json"
    local staged_image=$(jq -r '.status.staged.image.image.image // "none"' "$workdir/status-staged.json")
    log "Staged image: $staged_image"

    if [ "$staged_image" = "none" ]; then
        die "No staged deployment found after bootc switch"
    fi

    # Reboot VM
    log "Rebooting VM to activate new deployment"
    ssh_exec "$vm_ip" "sudo systemctl reboot" || true  # Expect disconnect
    sleep 10

    # Wait for VM to come back up
    log "Waiting for VM to reboot..."
    wait_for_ssh "$vm_ip" 120 || die "VM failed to reboot"

    # Verify new deployment is active
    log "Verifying new deployment after reboot"
    ssh_exec "$vm_ip" "sudo bootc status --json" > "$workdir/status-after.json"
    local final_image=$(jq -r '.status.booted.image.image.image' "$workdir/status-after.json" 2>/dev/null || echo "unknown")
    local final_digest=$(jq -r '.status.booted.image.imageDigest' "$workdir/status-after.json" 2>/dev/null || echo "unknown")

    log "Final booted image: $final_image"
    log "Final digest: $final_digest"

    # Get expected digest from reconstructed image
    local expected_digest=$(ssh_exec "$vm_ip" "skopeo inspect --format '{{.Digest}}' oci-archive:/var/home/$VM_USER/reconstructed.oci-archive")

    # Verify we're running the new deployment
    if [ "$final_digest" = "$expected_digest" ]; then
        log "${GREEN}✓ SUCCESS: VM rebooted into new deployment${NC}"

        # Show size comparison - demonstrates bandwidth savings
        local delta_size=$(ssh_exec "$vm_ip" "du -h /var/home/$VM_USER/update.oci-delta" | cut -f1)
        local target_size=$(ssh_exec "$vm_ip" "du -h /var/home/$VM_USER/target.oci-archive" | cut -f1)
        log "Bandwidth savings:"
        log "  Delta transmitted: $delta_size"
        log "  Full image would be: $target_size"

        return 0
    else
        error "Deployment verification failed"
        error "Expected digest: $expected_digest"
        error "Actual digest: $final_digest"
        error "Booted image: $final_image"
        return 1
    fi
}

# Container-based E2E test (no reboot verification)
test_container() {
    log "Running container-based E2E test (no reboot verification)"
    warn "Container mode cannot verify reboot cycle - consider running on KVM-enabled system for full coverage"

    log "Work directory: $workdir"

    # Pull base and target images
    log "Pulling base image: $BASE_IMAGE"
    podman pull "$BASE_IMAGE"

    log "Pulling target image: $TARGET_IMAGE"
    podman pull "$TARGET_IMAGE"

    # Export images
    log "Exporting images to OCI archives"
    podman save --format oci-archive -o "$workdir/base.oci-archive" "$BASE_IMAGE"
    podman save --format oci-archive -o "$workdir/target.oci-archive" "$TARGET_IMAGE"

    # Create delta
    log "Creating delta from $BASE_IMAGE to $TARGET_IMAGE"
    "$OCI_DELTA" create --debug \
        "$workdir/base.oci-archive" \
        "$workdir/target.oci-archive" \
        "$workdir/update.oci-delta" || die "Delta creation failed"

    local delta_size=$(du -h "$workdir/update.oci-delta" | cut -f1)
    local target_size=$(du -h "$workdir/target.oci-archive" | cut -f1)
    log "Delta size: $delta_size (vs full image: $target_size)"

    # Run bootc container
    log "Starting privileged bootc container"
    CTR_NAME="oci-delta-e2e-$$"

    podman run -d --rm \
        --name "$CTR_NAME" \
        --privileged \
        -v "$workdir:/workdir:Z" \
        -v "$PROJECT_DIR:/oci-delta:ro,Z" \
        "$BASE_IMAGE" \
        sleep infinity || die "Failed to start container"

    # Get initial status
    log "Checking initial bootc status"
    podman exec "$CTR_NAME" bootc status --json > "$workdir/status-before.json" 2>/dev/null || warn "bootc status failed (expected in container mode)"

    # Apply delta (same command as VM mode)
    log "Applying delta in container"
    podman exec "$CTR_NAME" /oci-delta/oci-delta apply --debug \
        /workdir/update.oci-delta \
        /workdir/reconstructed.oci-archive || die "Delta apply failed"

    # Verify output
    log "Verifying applied image"
    if ! skopeo inspect --raw "oci-archive:$workdir/reconstructed.oci-archive" > /dev/null 2>&1; then
        die "Applied image is not a valid OCI archive"
    fi

    # Try bootc switch (may not fully work in container, but tests the command)
    log "Testing bootc switch command"
    if podman exec "$CTR_NAME" bootc switch --transport=oci-archive /workdir/reconstructed.oci-archive 2>&1; then
        log "bootc switch succeeded"

        # Get final status
        podman exec "$CTR_NAME" bootc status --json > "$workdir/status-after.json" 2>/dev/null || true
    else
        warn "bootc switch failed in container mode (expected - containers cannot stage deployments)"
    fi

    # Verify delta output matches target image
    log "Verifying delta output matches target image"
    local target_digest=$(skopeo inspect --format '{{.Digest}}' "oci-archive:$workdir/target.oci-archive")
    local output_digest=$(skopeo inspect --format '{{.Digest}}' "oci-archive:$workdir/reconstructed.oci-archive")

    if [ "$target_digest" = "$output_digest" ]; then
        log "${GREEN}✓ SUCCESS: Delta apply produces correct image${NC}"
        log "Size comparison:"
        log "  Delta: $delta_size"
        log "  Full target image: $target_size"
        warn "Note: Reboot verification skipped in container mode"
        return 0
    else
        error "Output image does not match target"
        error "Expected: $target_digest"
        error "Actual: $output_digest"
        return 1
    fi
}

# Build bootc disk using bootc-image-builder
build_bootc_disk() {
    local image="$1" workdir="$2"

    log "Running bootc-image-builder for $image"

    # Create output directory
    mkdir -p "$workdir/qcow2"

    # Run bootc-image-builder
    local config_file="$SCRIPT_DIR/e2e-config.toml"
    sudo podman run --rm -t --privileged --pull=newer \
        -v "$workdir/qcow2:/output" \
        -v "$config_file:/config.toml:ro" \
        -v /var/lib/containers/storage:/var/lib/containers/storage \
        quay.io/centos-bootc/bootc-image-builder:latest \
        --config /config.toml \
        --type qcow2 \
        --rootfs ext4 \
        "$image" || return 1

    # Find the generated disk (bootc-image-builder puts it in qcow2/qcow2/)
    local disk=$(sudo find "$workdir/qcow2" -name "*.qcow2" | head -1)
    if [ -z "$disk" ]; then
        error "No qcow2 disk found after build"
        sudo ls -R "$workdir/qcow2/" || true
        return 1
    fi

    log "Found disk at: $disk"

    # Move to standard location and fix permissions
    if [ "$disk" != "$workdir/qcow2/disk.qcow2" ]; then
        sudo mv "$disk" "$workdir/qcow2/disk.qcow2"
    fi
    sudo chown $USER:$USER "$workdir/qcow2/disk.qcow2"

    # Resize disk to 40GB for E2E test (need space for base image + target image + archives + delta)
    log "Resizing disk to 40GB for test operations"
    qemu-img resize "$workdir/qcow2/disk.qcow2" 40G || warn "Failed to resize disk"

    log "Bootc disk created: $workdir/qcow2/disk.qcow2"
    return 0
}

# Create VM from disk
create_vm() {
    local name="$1" disk="$2"

    # Ensure default network is active (use system connection)
    sudo virsh net-info default &>/dev/null || sudo virsh net-start default 2>/dev/null || true

    # Copy disk to libvirt images directory (qemu can access it there)
    local libvirt_disk="/var/lib/libvirt/images/${name}.qcow2"
    log "Copying disk to libvirt images directory"
    sudo cp "$disk" "$libvirt_disk"
    sudo chown qemu:qemu "$libvirt_disk" 2>/dev/null || sudo chown libvirt-qemu:kvm "$libvirt_disk" 2>/dev/null || true

    # Import existing disk with virt-install (use system connection)
    # Use 8GB RAM for delta creation (oci-delta needs significant memory for processing large bootc images)
    sudo virt-install \
        --connect qemu:///system \
        --name "$name" \
        --memory 8192 \
        --vcpus 2 \
        --disk "$libvirt_disk" \
        --import \
        --os-variant fedora-unknown \
        --network network=default \
        --graphics none \
        --noautoconsole \
        --boot uefi

    sleep 10
    return 0
}

# Wait for VM to get IP address
wait_for_vm_ip() {
    local name="$1" timeout=120 elapsed=0

    log "Waiting up to ${timeout}s for VM to get IP..."

    while [ $elapsed -lt $timeout ]; do
        local state=$(sudo virsh -c qemu:///system domstate "$name" 2>/dev/null || echo "unknown")

        if [ "$state" = "running" ]; then
            local ip=$(sudo virsh -c qemu:///system domifaddr "$name" 2>/dev/null | awk '/ipv4/ {print $4}' | cut -d/ -f1 | head -1)
            if [ -n "$ip" ]; then
                echo "$ip"
                return 0
            fi
        else
            warn "VM state: $state (elapsed: ${elapsed}s)"
        fi

        sleep 5
        elapsed=$((elapsed + 5))
    done

    error "Timeout waiting for VM IP. Final state:"
    sudo virsh -c qemu:///system domstate "$name" 2>/dev/null || true
    sudo virsh -c qemu:///system domifaddr "$name" 2>/dev/null || true

    return 1
}

# Wait for SSH to be ready
wait_for_ssh() {
    local ip="$1" timeout="${2:-180}" elapsed=0

    log "Waiting up to ${timeout}s for SSH at $VM_USER@$ip..."

    while [ $elapsed -lt $timeout ]; do
        if sshpass -p "$VM_PASSWORD" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 "$VM_USER@$ip" "true" 2>/dev/null; then
            log "SSH is ready!"
            return 0
        fi

        # Show progress every 15 seconds
        if [ $((elapsed % 15)) -eq 0 ]; then
            log "Still waiting for SSH... (${elapsed}s elapsed)"
        fi

        sleep 5
        elapsed=$((elapsed + 5))
    done

    error "SSH timeout after ${timeout}s. Diagnostics:"
    log "Testing basic connectivity:"
    ping -c 3 "$ip" || true
    log "Attempting SSH with verbose output:"
    sshpass -p "$VM_PASSWORD" ssh -vv -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 "$VM_USER@$ip" "true" 2>&1 | head -30 || true

    return 1
}

# Execute command via SSH
ssh_exec() {
    local ip="$1"
    shift
    sshpass -p "$VM_PASSWORD" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "$VM_USER@$ip" "$@"
}

# Execute command via SSH with output streaming (for long-running commands)
ssh_exec_verbose() {
    local ip="$1"
    shift
    log "Running: $*"
    sshpass -p "$VM_PASSWORD" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "$VM_USER@$ip" "$@" 2>&1 | while IFS= read -r line; do
        echo "  [VM] $line" >&2
    done
    return ${PIPESTATUS[0]}
}

# Copy file to VM via SCP
scp_to_vm() {
    local ip="$1" src="$2" dst="$3"
    sshpass -p "$VM_PASSWORD" scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "$src" "$VM_USER@$ip:$dst"
}

# Cleanup VM and resources
cleanup_vm() {
    local name="$1"

    if sudo virsh -c qemu:///system list --all 2>/dev/null | grep -q "$name"; then
        log "Cleaning up VM: $name"
        sudo virsh -c qemu:///system destroy "$name" 2>/dev/null || true
        sudo virsh -c qemu:///system undefine "$name" --remove-all-storage 2>/dev/null || true
    fi

    local libvirt_disk="/var/lib/libvirt/images/${name}.qcow2"
    if [ -f "$libvirt_disk" ]; then
        sudo rm -f "$libvirt_disk"
    fi
}

# Main
main() {
    log "OCI-Delta E2E Test with Bootc"
    log "=============================="

    check_prereqs

    local mode=$(detect_mode)
    log "Test mode: $mode"

    case "$mode" in
        vm)
            for cmd in sshpass virt-install qemu-img; do
                if ! command -v "$cmd" &>/dev/null; then
                    die "$cmd is required for VM mode (install with: sudo dnf install $cmd)"
                fi
            done
            test_vm
            ;;
        container)
            test_container
            ;;
        *)
            die "Invalid test mode: $mode (use 'vm', 'container', or 'auto')"
            ;;
    esac
}

main "$@"
