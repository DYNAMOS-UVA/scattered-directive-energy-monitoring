SHELL := /bin/bash
targets := sql-query vfl-train-model vfl-train vfl-train-demo vfl-train-model-demo
datasets := "../configuration/datasets/"
branch_name := $(shell git rev-parse --abbrev-ref HEAD)
dockerhub_account := $(shell bash -c 'source ../dynamos.conf && echo $$DOCKERHUB_ACCOUNT')
PUSH ?= 1
#  default is 1 (do push to dockerhub) (the mechanism is a bit problematic because it reads whitespaces, so be careful)

BUILD_BOTH ?= 0

.PHONY: proto $(targets) dynamos vfl vfl-demo

vfl: vfl-train vfl-train-model
vfl-demo: vfl-train-demo vfl-train-model-demo

proto:
	@python3 -m grpc_tools.protoc -I../proto-files --python_out=./dynamos-python-lib/protofiles --grpc_python_out=./dynamos-python-lib/protofiles ../proto-files/*.proto

dynamos:
	(cd ./dynamos-python-lib && python3 setup.py bdist_wheel)

# Set PYTHONPATH to protofiles directory for includes to GRPC code to work
$(targets): proto
	cp Dockerfile ./$@

	# Build the wheel for the local package
	(cd ./dynamos-python-lib && python3 setup.py bdist_wheel)

	# Copy the wheel to the target directory
	cp ./dynamos-python-lib/dist/*.whl ./$@
	cp -r ./dynamos-python-lib/protofiles ./$@

	# Build Docker container
	@trap 'rm -f ./$@/Dockerfile; rm -rf ./$@/*.whl; rm -rf ./$@/protofiles' EXIT;  \
	WHEEL_NAME=$$(ls ./$@/*.whl); \
	BASE=$$(basename $$WHEEL_NAME); \
	if [ "$(BUILD_BOTH)" = "1" ]; then \
		docker build --platform linux/amd64 --build-arg NAME=$@ --build-arg WHEEL_NAME=$$BASE -t $(dockerhub_account)/$@:$(branch_name) -t $(dockerhub_account)/$@:latest ./$@/; \
		if [ "$(PUSH)" = "1" ]; then \
			docker push $(dockerhub_account)/$@:$(branch_name); \
			docker push $(dockerhub_account)/$@:latest; \
		fi; \
	else \
		docker build --platform linux/amd64 --build-arg NAME=$@ --build-arg WHEEL_NAME=$$BASE -t $(dockerhub_account)/$@:$(branch_name) ./$@/; \
		if [ "$(PUSH)" = "1" ]; then \
			docker push $(dockerhub_account)/$@:$(branch_name); \
		fi; \
	fi
	
all: $(targets) proto