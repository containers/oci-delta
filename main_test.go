package main

import "testing"

func TestRequiresUserNamespace(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{
			name: "When creating from OCI layouts it should not reexec",
			args: []string{"create", "oci:/old", "oci:/new", "oci:/out"},
			want: false,
		},
		{
			name: "When creating from OCI archives it should not reexec",
			args: []string{"create", "oci-archive:/old.tar", "oci-archive:/new.tar", "oci-archive:/delta.tar"},
			want: false,
		},
		{
			name: "When creating from container storage it should reexec",
			args: []string{"create", "containers-storage:localhost/old", "oci-archive:/new.tar", "oci-archive:/delta.tar"},
			want: true,
		},
		{
			name: "When applying with a container storage root it should reexec",
			args: []string{"apply", "--container-storage", "/var/lib/containers/storage", "delta", "oci:/out"},
			want: true,
		},
		{
			name: "When applying with an equals-form container storage root it should reexec",
			args: []string{"apply", "--container-storage=/var/lib/containers/storage", "delta", "oci:/out"},
			want: true,
		},
		{
			name: "When applying from a directory it should not reexec",
			args: []string{"apply", "--directory", "/src", "delta", "oci:/out"},
			want: false,
		},
		{
			name: "When importing it should reexec",
			args: []string{"import", "delta"},
			want: true,
		},
		{
			name: "When import help is requested it should not reexec",
			args: []string{"import", "--help"},
			want: false,
		},
		{
			name: "When top-level help is requested it should not reexec",
			args: []string{"help", "import"},
			want: false,
		},
		{
			name: "When no arguments are passed it should not reexec",
			args: nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requiresUserNamespace(tt.args); got != tt.want {
				t.Errorf("requiresUserNamespace(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
