docker buildx build -t wyze-bridge-test -f docker/Dockerfile . --build-arg BUILD=test --build-arg BUILD_DATE=today --build-arg GITHUB_SHA=NA --platform=linux/amd64

docker run -i -t --rm --mount type=bind,src=./media,dst=/media --env-file ./test_env.list wyze-bridge-test