BINARY ?= represents3
DOCKER_IMAGE ?= represents3:latest
GO ?= go

.PHONY: tidy fmt build run clean docker-build docker-up docker-down service-install service-enable service-start service-status

tidy:
	$(GO) mod tidy

fmt:
	$(GO) fmt ./...

build: tidy
	$(GO) build -o $(BINARY) .

run:
	$(GO) run .

clean:
	rm -f $(BINARY)

docker-build:
	docker build -t $(DOCKER_IMAGE) .

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

service-install:
	sudo install -m 0755 $(BINARY) /usr/local/bin/$(BINARY)
	sudo install -d -m 0755 /etc/represents3
	sudo install -m 0644 .env.example /etc/represents3/env
	sudo install -m 0644 systemd/represents3.service /etc/systemd/system/represents3.service
	sudo systemctl daemon-reload

service-enable:
	sudo systemctl enable represents3

service-start:
	sudo systemctl restart represents3

service-status:
	sudo systemctl status represents3 --no-pager
