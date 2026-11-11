STANDARDS_DIR ?= $(wildcard $(CURDIR)/../go-standards)
include standard.mk

CHECK_EXTRA := modverify linux fuzz

.PHONY: modverify linux fuzz

modverify:
	$(GO) mod verify

linux:
	GOOS=linux GOARCH=amd64 $(GO) build ./...

# Canonical JSON must be stable for arbitrary values (TST-4).
fuzz:
	$(GO) test -run='^$$' -fuzz=FuzzCanonicalIsStable -fuzztime=20s ./evidence
