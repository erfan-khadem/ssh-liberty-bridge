# SSH-Liberty-Bridge

## QuickStart Guide

First, install the python requirements using:

```bash
python3 -m pip install -r generator/requirements.txt
```

Note that in some systems, `python-dotenv` package cannot be installed manually. In that
case you should install it using your distribution's package manager.

Then copy `env-sample` as `.env` and change its variables according to your
own needs. For example you may need to change `TEMPLATE_PATH` if you want to
use your own template file instead of the one at `generator/template.json`.

Initialize the required directories:
```bash
# Run the following commands as Root
mkdir -p /tmp/etc/ssh/
mkdir -p /etc/ssh-liberty-bridge/
```

Then generate host keys for your `ssh-server`. Please put these keys in a safe
place and never share the ones that don't end in `.pub` (your public keys) with others.

```bash
# Run the following commands as Root
ssh-keygen -A -f /tmp  # Creates the required keys in /tmp/etc/ssh

# Remove unneeded key pairs
rm /tmp/etc/ssh/ssh_host_dsa_key*
rm /tmp/etc/ssh/ssh_host_rsa_key*

# Copy the keys to the installation directory
cp /tmp/etc/ssh/* /etc/ssh-liberty-bridge/

# Delete the temporary key files and make sure they are not recoverable
shred /tmp/etc/ssh/*
rm /tmp/etc/ssh/*
```

And change file ownership and permissions so only your user could read the created files.
```bash
# Run these commands from your user but use sudo.
# Or run as root without sudo and write your username instead of `$USER`
sudo chown -R $USER:$USER /etc/ssh-liberty-bridge/
sudo chmod 0600 /etc/ssh-liberty-bridge/*
```

After this, you have to install `redis` on your server. After doing so, it is of *utmost importance*
that you add a strong password to its configuration. In order to achieve this, you have to
navigate to `/etc/redis/redis.conf` and edit the following line
(in my installation, this is line 1036, it may not be exactly this for you)

```
# requirepass foobard
```

to something like:

```
requirepass my_strong_and_long_password
```

You should also adjust your `.env` file to reflect your chosen password. It is not recommended to
add special characters to your password. We do this because by default connected users can access
our local network (even if we add basic restrictions it can still be bypassed,
so lets not endanger our servers by not using a strong password)

After doing this, don't forget to start and enable redis by running

```bash
# Run this as root
systemctl enable --now redis.service
```

Now 
- you can download the latest release file 
```bash
wget https://github.com/hiddify/ssh-liberty-bridge/releases/latest/download/ssh-liberty-bridge-$(dpkg --print-architecture)
mv ssh-liberty-bridge-* ssh-liberty-bridge
chmod +x ssh-liberty-bridge
```
or 
- you can build and run the server.

```bash
go build main.go
./main /path/to/.env
```

After running the server, you can generate configs for your clients.

First, make sure that `TEMPLATE_PATH` in your `.env` is correctly set.
Then run the following command to see the supported commands by your configuration generator:

```bash
python3 main.py --help
```

Note that almost any variable specified by `.env` file can be overridden using the cli interface
of the generator or normal environmental variables. Also if the `.env` file is not in its usual
location, you may provide it to your code using the `--env` flag. You may also have to run the commands as root to access your user config path. In this case you also have to install python requirements from the first step as root.

For example, to add 5 new users, do the following:

```bash
python3 main.py --env /path/to/.env --add 5
```

And to list the available configurations, run:

```bash
python3 main.py --env /path/to/.env --list
```

And to remove a configuration:

```bash
python3 main.py --env /path/to/.env --rem (UUID of the client to remove from above)
```
