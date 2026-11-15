# Proxer Agent Skill Makefile

APP_NAME := proxer
SKILL_DIR_NAME := proxer
BUILD_DIR := ./bin
MAIN_PATH := ./cmd/proxer
SKILL_SRC := ./skills/$(SKILL_DIR_NAME)

.PHONY: all
all: clean build

.PHONY: build
build:
	@echo "Building $(APP_NAME) executable (CGO-free)..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build -o $(BUILD_DIR)/$(APP_NAME) $(MAIN_PATH)
	@echo "Build complete: $(BUILD_DIR)/$(APP_NAME)"

.PHONY: test
test:
	@echo "Running unit tests..."
	go test ./... -v

.PHONY: clean
clean:
	@echo "🧹 Cleaning up build artifacts..."
	@rm -rf $(BUILD_DIR)

# Usage: make install DEST=/path/to/your/target/project
.PHONY: install
install: build
	@if [ -z "$(DEST)" ]; then \
		echo "Error: DEST variable is not set."; \
		echo "Usage: make install DEST=/path/to/your/target-project"; \
		exit 1; \
	fi
	@echo "Constructing Agent Skill micro-package in $(DEST)..."
	@if [ ! -f $(SKILL_SRC)/SKILL.md ]; then \
		echo "Error: missing $(SKILL_SRC)/SKILL.md"; \
		exit 1; \
	fi
	@mkdir -p $(DEST)/.agents/skills/$(SKILL_DIR_NAME)/scripts
	@echo "   - Injecting cognitive entry point (SKILL.md)..."
	@cp -R $(SKILL_SRC)/. $(DEST)/.agents/skills/$(SKILL_DIR_NAME)/
	@echo "   - Injecting deterministic binary (proxer)..."
	@cp $(BUILD_DIR)/$(APP_NAME) $(DEST)/.agents/skills/$(SKILL_DIR_NAME)/scripts/$(APP_NAME)
	@echo "Agent Skill [$(SKILL_DIR_NAME)] installed successfully!"
	@echo "Binary: $(DEST)/.agents/skills/$(SKILL_DIR_NAME)/scripts/$(APP_NAME)"
