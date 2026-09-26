SHELL := /bin/bash

SERVER_DIR := server
CONTROLLER_CHART := deploy/helm/jobpilot-controller
CATALOG_CHART := deploy/helm/jobpilot-jobs-catalog

.PHONY: test test-race vet build lint template verify

test:
	cd $(SERVER_DIR) && go test ./...

test-race:
	cd $(SERVER_DIR) && go test -race ./...

vet:
	cd $(SERVER_DIR) && go vet ./...

build:
	cd $(SERVER_DIR) && go build -o /tmp/jobpilot-controller ./cmd/controller

lint:
	helm lint $(CONTROLLER_CHART)
	helm lint $(CATALOG_CHART)

template:
	helm template jobpilot $(CONTROLLER_CHART) --namespace jobpilot > /tmp/jobpilot-controller.yaml
	helm template catalog $(CATALOG_CHART) --namespace jobpilot > /tmp/jobpilot-catalog.yaml

verify: test vet build lint template
