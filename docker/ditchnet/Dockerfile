FROM williamlidberg/ditchnet
RUN apt-get update
RUN apt-get install -y bash wget tar gcc git
RUN wget https://go.dev/dl/go1.18.3.linux-amd64.tar.gz -P /tmp/
RUN tar -C /usr/local -xzf /tmp/go1.18.3.linux-amd64.tar.gz
ENV PATH="${PATH}:/usr/local/go/bin"
RUN go version
RUN mkdir /var/ditchnet
RUN cd /var && \
	git pull https://github.com/Froglich/ditchnet-web.git && \
	cd ditchnet-web && \
	go mod tidy && \
	go build
COPY config.json /var/ditchnet-web/config.json
CMD ["/var/ditchnet-web/ditchnet-web", "/var/ditchnet-web/config.json"]