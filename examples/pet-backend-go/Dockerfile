# Build from the repository root: docker build -f examples/pet-backend-go/Dockerfile .
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/pet-backend ./examples/pet-backend-go

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pet-backend /pet-backend
USER nonroot
EXPOSE 9090
ENTRYPOINT ["/pet-backend"]
