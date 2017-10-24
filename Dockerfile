FROM golang

# Note that these env variables are visible via `docker history`, 'docker inspect`.
# Never upload to public registry; only ECR (current).
ARG awsrgn
ARG awsid
ARG awssec
ARG pullrsns
ARG pullrsqs
ENV AWS_REGION=$awsrgn \
    AWS_ACCESS_KEY_ID=$awsid \
    AWS_SECRET_ACCESS_KEY=$awssec \
    PULLR_SNS_ARN=$pullrsns \
    PULLR_SQS_URL=$pullrsqs
ADD . /go/src/pullr-icarium
WORKDIR /go/src/pullr-icarium
RUN make
ENTRYPOINT ["/go/src/pullr-icarium/bin/icariumd"]
