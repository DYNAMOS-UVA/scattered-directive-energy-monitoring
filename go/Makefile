branch_name := $(shell git rev-parse --abbrev-ref HEAD)
dynamos_core := sidecar orchestrator agent api-gateway
dynamos := $(dynamos_core) policy-enforcer
dockerhub_account := dynamos1
platform ?= linux/amd64
eflint_repo ?= https://gitlab.com/eflint/haskell-implementation.git
eflint_tag ?= v4.1.0.1

prepare:
	go mod tidy
	go mod download

proto:
	protoc -I ../proto-files --go_out=./pkg/proto --go_opt=paths=source_relative --go-grpc_out=./pkg/proto --go-grpc_opt=paths=source_relative ../proto-files/*.proto

$(dynamos_core): prepare proto
	cp Dockerfile go.mod go.sum ./cmd/$@
	cp -r pkg ./cmd/$@
	(trap 'rm -f ./cmd/$@/Dockerfile; rm -f ./cmd/$@/go.*; rm -rf ./cmd/$@/pkg' EXIT; \
	docker build --platform linux/amd64 --build-arg NAME='$@' -t $(dockerhub_account)/$@:$(branch_name) -t $(dockerhub_account)/$@:latest ./cmd/$@/)
	docker push $(dockerhub_account)/$@:$(branch_name)
	docker push $(dockerhub_account)/$@:latest
endef

$(dynamos): prepare proto
	$(build_and_push_image)

policy-enforcer: prepare proto
	docker buildx build --platform $(platform) --load --build-arg NAME='policy-enforcer' --build-arg EFLINT_REPO='$(eflint_repo)' --build-arg EFLINT_TAG='$(eflint_tag)' -t $(dockerhub_account)/policy-enforcer:$(branch_name) -t $(dockerhub_account)/policy-enforcer:latest -f ./cmd/policy-enforcer/Dockerfile .
	docker push $(dockerhub_account)/policy-enforcer:$(branch_name)
	docker push $(dockerhub_account)/policy-enforcer:latest


$(sql_microservices): prepare proto
	$(build_and_push_image)

all: $(dynamos) $(sql_microservices)
dynamos: $(dynamos)

.PHONY: all prepare proto $(dynamos)
