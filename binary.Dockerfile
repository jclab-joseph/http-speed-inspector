FROM alpine:3.21
COPY dist/server/ /tmp/dist/
RUN mv /tmp/dist/server-linux-$(uname -m).exe /server.exe && \
    rm -rf /tmp/dist/

EXPOSE 3000
CMD /server.exe
