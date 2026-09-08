FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/transflux ./cmd/transflux

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/transflux /transflux
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/transflux"]
