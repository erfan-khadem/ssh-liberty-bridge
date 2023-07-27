# ssh-server

A WIP custom ssh-server implementation. Supports various user monitoring options.
To create the required server keys, run the following command:
```bash
mkdir -p ./etc/ssh/
ssh-keygen -A -f .
```

# Running notes

- In case `tcpip-direct` is enabled, great care should be taken as the clients would
be able to connect to arbitrary services running locally on the server.
