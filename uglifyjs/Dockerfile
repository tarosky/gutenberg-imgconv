ARG nodejs_version
FROM node:$nodejs_version-buster

ARG uid
ARG gid

RUN npm install --no-update-notifier -g nexe

COPY uglifyjs/entrypoint /entrypoint

RUN mkdir -p /workspace /ephemeral
COPY uglifyjs/dummy.package.json /ephemeral/package.json
RUN chown -R $uid:$gid /workspace /ephemeral

WORKDIR /ephemeral

ENTRYPOINT [ "/entrypoint" ]
