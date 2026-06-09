# class-operator

The class operator is a Kubernetes operator that will deploy the infrastructure necessary to run classes in a cloud environment, as a way to maintain the Open Education Project (OPE) for future use.

This operator will allow for class infrastructure to be easily deployed and managed through a declarative interface, enabling administrators to define class environments, user access, and resource policies in a scalable and reproducible way.

![alt text](images/image.png)


## How to install the operator

1. Login into docker and openshift

    `docker login quay.io -u <your-username>`

    `oc login --token=<you-token> --server=<your-server>`

2. Generate CRDs and Test

    `make manifests`

3. Run tests

    `make test`

4. Build and Push the Image

    `make docker-build`

    `make docker-push`

5. Deploy Operator
  
    `make deploy`

6. Install CRD
  
    `make install`