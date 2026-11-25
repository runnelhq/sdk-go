# Go-SDK für Runnel-Plugins. Ziele: make test | lint | fuzz | bench | runnel-test
GOLANGCI_LINT_VERSION := v2.15.0
# Ohne passendes golangci-lint im PATH: make lint GOLANGCI_LINT=bin/tools/golangci-lint
GOLANGCI_LINT ?= golangci-lint
FUZZTIME      ?= 30s
# Runnel für die Tests gegen den Host, z. B. RUNNEL_BIN=../runnel/bin/runnel
RUNNEL_BIN    ?=

.PHONY: test lint fmt fuzz bench deps runnel-test

## test: alle Tests mit Race-Detector (Tests gegen Runnel nur mit RUNNEL_BIN)
test:
	RUNNEL_BIN=$(RUNNEL_BIN) go test -race ./...

## lint: golangci-lint für Linux- und Windows-Code
lint:
	$(GOLANGCI_LINT) run ./...
	GOOS=windows $(GOLANGCI_LINT) run ./...

## deps: prüfen, dass das SDK keine Abhängigkeiten hat (nur die Go-Standardbibliothek)
deps:
	scripts/check-no-deps.sh

## fmt: Quelltext formatieren
fmt:
	$(GOLANGCI_LINT) fmt ./...

## fuzz: Protokoll-Schleife mit zufälligen Eingaben
fuzz:
	go test -run '^$$' -fuzz FuzzServe -fuzztime $(FUZZTIME) .

## bench: Durchsatz eines Transforms über das Protokoll (ohne Runnel)
bench:
	go test -run '^$$' -bench . -benchmem .

## runnel-test: nur die Tests gegen Runnel (RUNNEL_BIN nötig)
runnel-test:
	@test -n "$(RUNNEL_BIN)" || (echo "set RUNNEL_BIN to the runnel program" && exit 2)
	RUNNEL_BIN=$(abspath $(RUNNEL_BIN)) go test -count=1 -run WithRunnel ./...

bin/tools/golangci-lint:
	GOBIN=$(CURDIR)/bin/tools go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
