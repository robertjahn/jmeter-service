# Use the offical Golang image to create a build artifact.
# This is based on Debian and sets the GOPATH to /go.
# https://hub.docker.com/_/golang
FROM golang:1.12 as builder

# Copy local code to the container image.
WORKDIR /go/src/github.com/keptn/jmeter-service
COPY . .

ARG DEP_VERSION=0.5.3
RUN curl -L -s https://github.com/golang/dep/releases/download/v$DEP_VERSION/dep-linux-amd64 -o ./dep && \
  chmod +x ./dep && \
  ./dep ensure

# Build the command inside the container.
# (You may fetch or manage dependencies here,
# either manually or with a tool like "godep".)
RUN CGO_ENABLED=0 GOOS=linux go build -v -o jmeter-service

# Use a Docker multi-stage build to create a lean production image.
# https://docs.docker.com/develop/develop-images/multistage-build/#use-multi-stage-builds
FROM justb4/jmeter:5.1.1
RUN apk add --no-cache ca-certificates

ARG KUBE_VERSION=1.14.1
RUN wget -q https://storage.googleapis.com/kubernetes-release/release/v$KUBE_VERSION/bin/linux/amd64/kubectl -O /bin/kubectl && \
  chmod +x /bin/kubectl

# Copy the binary to the production image from the builder stage.
COPY --from=builder /go/src/github.com/keptn/jmeter-service/jmeter-service /jmeter-service
ADD MANIFEST /
# Run the web service on container startup.
CMD ["sh", "-c", "cat MANIFEST && /jmeter-service"]