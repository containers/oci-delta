.PHONY: build clean test test-coverage test-e2e fmt lint man

COVERDIR ?= $(CURDIR)/.coverdata

VERSION := 0.99.999

build:
	go build

man:
	go-md2man -in docs/man/oci-delta.1.md -out docs/man/oci-delta.1

clean:
	rm -f oci-delta
	rm -f docs/man/oci-delta.1
	rm -rf $(COVERDIR)
	rm -rf $(CURDIR)/rpmbuild
	rm -f build/package/rpm/*-vendor.tar.bz2
	rm -f build/package/rpm/oci-delta-*.tar.gz
	rm -f config.toml

test: build
	go test ./...
	python3 tests/test-synthetic.py
	tests/integration-test.sh

test-coverage:
	go build -cover -o oci-delta
	rm -rf $(COVERDIR) && mkdir -p $(COVERDIR)/unit $(COVERDIR)/integration
	go test -cover ./... -args -test.gocoverdir=$(COVERDIR)/unit
	GOCOVERDIR=$(COVERDIR)/integration python3 tests/test-synthetic.py
	GOCOVERDIR=$(COVERDIR)/integration tests/integration-test.sh
	go tool covdata percent -i $(COVERDIR)/unit,$(COVERDIR)/integration

test-e2e: build
	@echo "Running E2E tests (requires KVM for full coverage, falls back to container mode)"
	tests/e2e-bootc-test.sh

fmt:
	go fmt ./...

lint:
	go vet ./...
	golangci-lint run --fix -c .golangci.yml

install:
	go install

#
# RPM packaging
#

RPM_PKGDIR = build/package/rpm
RPM_SPECFILE = $(RPM_PKGDIR)/oci-delta.spec
RPM_TOML = $(RPM_PKGDIR)/go-vendor-tools.toml
RPM_TOPDIR = $(CURDIR)/rpmbuild

VENDOR_TAR = $(RPM_PKGDIR)/oci-delta-$(VERSION)-vendor.tar.bz2

.PHONY: vendor-tarball
vendor-tarball:
	git archive --prefix=oci-delta-$(VERSION)/ HEAD | gzip > $(RPM_PKGDIR)/oci-delta-$(VERSION).tar.gz
	go_vendor_archive create \
		-c $(RPM_TOML) \
		$(RPM_SPECFILE)
	$(eval TMPDIR := $(shell mktemp -d))
	tar xf $(VENDOR_TAR) -C $(TMPDIR)
	go_vendor_license \
		-c $(RPM_TOML) \
		-C $(RPM_SPECFILE) \
		report \
		--verify-spec \
		> $(TMPDIR)/vendor/LICENSE-BREAKDOWN.txt
	tar cjf $(VENDOR_TAR) -C $(TMPDIR) vendor
	rm -rf $(TMPDIR)

.PHONY: srpm
srpm: vendor-tarball
	mkdir -p $(RPM_TOPDIR)/SOURCES
	cp $(RPM_PKGDIR)/oci-delta-*.tar.gz $(RPM_PKGDIR)/*-vendor.tar.bz2 $(RPM_TOML) $(RPM_TOPDIR)/SOURCES/
	rpmbuild -bs \
		--define "_topdir $(RPM_TOPDIR)" \
		--with tests \
		$(RPM_SPECFILE)

.PHONY: rpm
rpm: vendor-tarball
	mkdir -p $(RPM_TOPDIR)/SOURCES
	cp $(RPM_PKGDIR)/oci-delta-*.tar.gz $(RPM_PKGDIR)/*-vendor.tar.bz2 $(RPM_TOML) $(RPM_TOPDIR)/SOURCES/
	rpmbuild -bb \
		--define "_topdir $(RPM_TOPDIR)" \
		--with tests \
		$(RPM_SPECFILE)

.PHONY: scratch
scratch: vendor-tarball
	mkdir -p $(RPM_TOPDIR)/SOURCES
	cp $(RPM_PKGDIR)/oci-delta-*.tar.gz $(RPM_PKGDIR)/*-vendor.tar.bz2 $(RPM_TOML) $(RPM_TOPDIR)/SOURCES/
	rpmbuild -bb \
		--define "_topdir $(RPM_TOPDIR)" \
		--without tests \
		--nocheck \
		$(RPM_SPECFILE)
