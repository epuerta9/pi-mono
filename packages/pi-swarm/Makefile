.PHONY: build run clean test

BINARY := pi-swarm
BUILD_DIR := ./bin

build:
	go build -o $(BUILD_DIR)/$(BINARY) ./cmd/pi-swarm

run: build
	$(BUILD_DIR)/$(BINARY) --agent-id pi-alpha

# Start a 3-node local cluster for testing
cluster: build
	@echo "Starting 3-node local cluster..."
	@$(BUILD_DIR)/$(BINARY) --agent-id pi-alpha --port 4222 --cluster-port 6222 &
	@sleep 2
	@$(BUILD_DIR)/$(BINARY) --agent-id pi-beta --port 4223 --cluster-port 6223 --seeds "nats://localhost:6222" &
	@sleep 2
	@$(BUILD_DIR)/$(BINARY) --agent-id pi-gamma --port 4224 --cluster-port 6224 --seeds "nats://localhost:6222,nats://localhost:6223" &
	@echo "Cluster started: pi-alpha(:4222), pi-beta(:4223), pi-gamma(:4224)"

test:
	go test ./...

clean:
	rm -rf $(BUILD_DIR)
	rm -rf /tmp/pi-swarm-test-*
