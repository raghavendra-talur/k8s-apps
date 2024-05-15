all: k8s-apps
	go build && rm sample && ./k8s-apps > sample