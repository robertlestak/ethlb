FROM golang:1.17 as builder

WORKDIR /src

COPY . .

RUN go build -o ethlb .

FROM golang:1.17 as app

WORKDIR /app

COPY --from=builder /src/ethlb /app/ethlb

ENTRYPOINT [ "/app/ethlb" ]