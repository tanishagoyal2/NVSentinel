# Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

FROM public.ecr.aws/docker/library/golang:1.25-bookworm AS builder

ARG VERSION="dev"
ARG GIT_COMMIT="none"
ARG BUILD_DATE=$(date -u +%FT%TZ)

WORKDIR /go/src/nvsentinel

# Copy the go.mod files for dependencies first
COPY fault-remediation/go.mod fault-remediation/go.sum fault-remediation/
COPY store-client/go.mod store-client/go.sum ./store-client/
COPY data-models/go.mod data-models/go.sum ./data-models/
COPY commons/go.mod commons/go.sum ./commons/

# Download dependencies
RUN --mount=type=cache,target=/go/pkg/mod \
    cd fault-remediation && go mod download

# Copy the source code
COPY fault-remediation/ fault-remediation/
COPY store-client/ store-client/
COPY data-models/ data-models/
COPY commons/ commons/

# Build the binary
RUN cd fault-remediation && \
    CGO_ENABLED=0 GOOS=linux go build -a -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${GIT_COMMIT} -X main.date=${BUILD_DATE}" -o fault-remediation main.go

# Runtime stage
FROM public.ecr.aws/docker/library/debian:bookworm-slim AS runtime

# Install ca-certificates for secure connections
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /go/src/nvsentinel/fault-remediation/fault-remediation /app/fault-remediation

ENTRYPOINT ["/app/fault-remediation"]
