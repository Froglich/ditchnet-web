FROM williamlidberg/ditchnet
RUN apt-get update
RUN apt-get install -y bash wget tar gcc git
RUN wget https://go.dev/dl/go1.18.3.linux-amd64.tar.gz -P /tmp/
RUN tar -C /usr/local -xzf /tmp/go1.18.3.linux-amd64.tar.gz
ENV PATH="${PATH}:/usr/local/go/bin"
RUN go version
RUN cd /var && \
	git clone https://github.com/Froglich/ditchnet-web.git && \
	cd ditchnet-web && \
	go mod tidy && \
	go build
COPY config.json /var/ditchnet-web/config.json

RUN mkdir /min/
RUN mkdir /min/input
RUN mkdir /min/output
RUN mkdir /min/modell/
RUN mkdir /min/temp_dir/
COPY testtile/ /min/modell/testtile
COPY utils/ /min/modell/utils
COPY DitchNet_1m.h5 /min/modell/
COPY DitchNet_05m.h5 /min/modell/
COPY script.py /min/modell/
RUN pip install whitebox==1.4.0
RUN python -c "exec('try:import whitebox\nexcept: pass')"

CMD ["/var/ditchnet-web/ditchnet-web", "/var/ditchnet-web/config.json"]