.PHONY: lint breaking generate build test check

lint:
	pnpm proto:lint

breaking:
	pnpm proto:breaking

generate:
	pnpm proto:gen
	pnpm build

build:
	pnpm build

test:
	pnpm test

check: lint generate test
	git diff --exit-code -- gen/
