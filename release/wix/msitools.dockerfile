FROM alpine:3.20
RUN apk update && apk add jq msitools
WORKDIR /workspace