.PHONY: build clean install

build:
	go build -o ass .

clean:
	rm -f ass

install: clean
	go install .
