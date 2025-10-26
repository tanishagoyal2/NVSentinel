# Tests Makefile
# End-to-end test targets

# Go binary
GO := go

# Default target
.PHONY: all
all: test

# Run end-to-end tests in CI
.PHONY: test-ci
test-ci:
	nohup kubectl port-forward -n monitoring svc/prometheus-operator-kube-p-prometheus 9090:9090 > /dev/null 2>&1 &
	nohup kubectl port-forward -n nvsentinel svc/simple-health-client 8080:8080 > /dev/null 2>&1 &
	nohup kubectl port-forward -n nvsentinel svc/nvsentinel-mongodb-headless 27017:27017 > /dev/null 2>&1 &
	@echo "Extracting MongoDB client certificates..."
	@mkdir -p /tmp/mongo-certs
	@kubectl get secret -n nvsentinel mongo-app-client-cert-secret -o jsonpath='{.data.tls\.crt}' | base64 -d > /tmp/mongo-certs/tls.crt
	@kubectl get secret -n nvsentinel mongo-app-client-cert-secret -o jsonpath='{.data.tls\.key}' | base64 -d > /tmp/mongo-certs/tls.key
	@kubectl get secret -n nvsentinel mongo-app-client-cert-secret -o jsonpath='{.data.ca\.crt}' | base64 -d > /tmp/mongo-certs/ca.crt
	@echo "Waiting for port-forwards to establish..."
	@sleep 3
	kubectl get po -Ao wide
	kubectl get no -o wide
	kubectl rollout restart deploy -l app.kubernetes.io/name=fault-quarantine -n nvsentinel
	$(MAKE) test
	@echo "Cleaning up certificates..."
	@rm -rf /tmp/mongo-certs

# Run end-to-end tests
.PHONY: test
test:
	MONGODB_TLS_SKIP_VERIFY=true $(GO) test -timeout 20m -count 1 -v ./...

# Help target
.PHONY: help
help:
	@echo "Tests Makefile - Simple end-to-end test execution"
	@echo ""
	@echo "Targets:"
	@echo "  all       - Run test (default)"
	@echo "  test-ci   - Run end-to-end tests in CI environment"
	@echo "  test      - Run end-to-end tests"
	@echo "  help      - Show this help message"
