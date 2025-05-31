.PHONY: cn-build-and-push-docker
cn-build-and-push-docker:
	@echo "set buildx builder"
	docker buildx create --name container-builder --driver docker-container --bootstrap --use || true

	@echo "build amd64/arm64 multi platform docker container"
	tar -ch . | \
	docker buildx build - \
	--platform linux/amd64,linux/arm64 \
	--tag codenow-codenow-releases.jfrog.io/codenow/grafana/grafana-operator:$(IMAGE_VERSION) \
	--output=type=image,push=true \
	--push \
	$(DOCKER_BUILD_ARGS)
