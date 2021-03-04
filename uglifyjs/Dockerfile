ARG nodejs_version
FROM node:$nodejs_version-stretch

ARG uid
ARG gid

RUN npm install -g nexe

COPY uglifyjs/entrypoint /entrypoint

RUN mkdir -p /workspace /ephemeral
COPY uglifyjs/dummy.package.json /ephemeral/package.json
RUN chown -R $uid:$gid /workspace /ephemeral

WORKDIR /ephemeral

ENTRYPOINT [ "/entrypoint" ]
