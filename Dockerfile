# syntax=docker/dockerfile:1

# ---- build: binario estático puro Go (sin CGO; sqlite es modernc) ----
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /virginbot .

# ---- runtime: imagen mínima (sin shell ni paquetes) ----
# La zona horaria va embebida en el binario (time/tzdata), así que no hace falta
# tzdata en la imagen. La BD se escribe en /data (volumen persistente).
FROM gcr.io/distroless/static-debian12
WORKDIR /data
COPY --from=build /virginbot /virginbot
ENV ADDR=":8080"
ENV VIRGINBOT_DB="/data/virginbot.db"
EXPOSE 8080
ENTRYPOINT ["/virginbot"]
