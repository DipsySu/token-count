VERSION ?= 0.3.0
LDFLAGS := -s -w -X main.version=$(VERSION)
PREFIX  ?= $(HOME)/.local
BINDIR  ?= $(PREFIX)/bin

# The real binary. Everything else is a symlink to it.
BIN ?= tcount

# Extra names to install alongside $(BIN). Override to taste:
#   make install ALIASES="token-count tokenSummary tsum"
#   make install ALIASES=              # just $(BIN), no aliases
# The binary reports whichever name it was invoked by, so every alias is
# first-class in the help text and in the UI.
ALIASES ?= token-count

.PHONY: build test install uninstall aliases

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) .

test:
	go test ./...

install: build
	install -d $(BINDIR)
	install -m 755 $(BIN) $(BINDIR)/$(BIN)
	@for a in $(ALIASES); do \
		if [ "$$a" != "$(BIN)" ]; then \
			ln -sfn $(BIN) $(BINDIR)/$$a && echo "aliased $(BINDIR)/$$a -> $(BIN)"; \
		fi; \
	done

# Remove every symlink in BINDIR that points at our binary, so aliases from an
# earlier install are cleaned up even if ALIASES has changed since.
uninstall:
	@for f in $(BINDIR)/*; do \
		if [ -L "$$f" ] && [ "$$(readlink "$$f")" = "$(BIN)" ]; then \
			rm -f "$$f" && echo "removed alias $$f"; \
		fi; \
	done
	@rm -f $(BINDIR)/$(BIN) && echo "removed $(BINDIR)/$(BIN)"

# Show which names are currently installed.
aliases:
	@echo "$(BINDIR)/$(BIN)"
	@for f in $(BINDIR)/*; do \
		if [ -L "$$f" ] && [ "$$(readlink "$$f")" = "$(BIN)" ]; then echo "$$f"; fi; \
	done
