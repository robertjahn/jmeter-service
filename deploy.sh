#!/bin/sh
kubectl delete -f deploy/service.yaml --ignore-not-found
kubectl apply -f deploy/service.yaml
