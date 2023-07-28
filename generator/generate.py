import os
import json
import glob
import uuid
import redis
import dotenv
import pathlib
import argparse

from string import Template
from generator.settings import USERS_SET, USERS_USAGE, DEFAULT_CONFIG_PATH
from generator.options import Options
from generator.utils import is_valid_uuid_v4, get_variable

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ed25519

redis_client: redis.Redis = None

def get_client_usage(client_uuid: str, reset: bool = False) -> int:
    global redis_client
    value = redis_client.hget(USERS_USAGE, client_uuid)

    if reset:
        redis_client.hincrby(USERS_USAGE, client_uuid, -value)
    return value

def get_client_addr(host_addr: str, client_uuid: str) -> str:
    return host_addr.format(uuid=client_uuid)


def generate(
    path: pathlib.Path, args: argparse.Namespace, client_uuid: str | None = None
) -> str:
    global redis_client

    if client_uuid is None:
        client_uuid = str(uuid.uuid4())

    if not is_valid_uuid_v4(client_uuid):
        raise RuntimeError("Invalid UUIDv4 supplied")

    server_addr = get_variable("server_addr", args, strict=True)
    server_port = get_variable("server_port", args, strict=True)
    host_key_path = get_variable("host_key_path", args, strict=True)
    template_path = get_variable("template_path", args, strict=True)

    host_keys = []
    template = None

    assert host_key_path.endswith("/")

    key_files = glob.glob(host_key_path + "*_key.pub")

    for file_name in key_files:
        with open(file_name, "r") as f:
            host_key = f.read().strip()
            host_key = host_key.split()
            if len(host_key) > 2:
                host_key = host_key[:2]  # strip the hostname part
            host_key = " ".join(host_key)
            host_keys.append(host_key)

    assert len(host_keys) > 0

    with open(template_path, "r") as f:
        template = Template(f.read())

    assert not template is None

    privkey = ed25519.Ed25519PrivateKey.generate()
    pubkey = privkey.public_key()
    priv_bytes = privkey.private_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PrivateFormat.OpenSSH,
        encryption_algorithm=serialization.NoEncryption(),
    )
    pub_bytes = pubkey.public_bytes(
        encoding=serialization.Encoding.OpenSSH,
        format=serialization.PublicFormat.OpenSSH,
    )

    client_string = client_uuid + "::" + pub_bytes.decode()
    if redis_client.sadd(USERS_SET, client_string) != 1:
        raise RuntimeError("Cannot save the public key")

    client_config = template.substitute(
        server_addr=server_addr,
        server_port=server_port,
        client_uuid=client_uuid,
        private_key=json.dumps(priv_bytes.decode()),
        host_keys=json.dumps(host_keys),
    )

    with open(path / (client_uuid + ".json"), "w") as f:
        f.write(client_config)

    redis_client.save()

    return client_uuid


def remove_client(path: pathlib.Path, client_uuid: str) -> None:
    """
    Remove the client with the specified UUID
    """
    global redis_client
    if not is_valid_uuid_v4(client_uuid):
        raise RuntimeError("Invalid UUIDv4 supplied")

    members = redis_client.smembers(USERS_SET)
    client_string = None
    for m in members:
        if m.startswith(client_uuid):
            client_string = m
            break

    if client_string is None:
        raise RuntimeError("Cannot find the specified client")

    redis_client.srem(USERS_SET, client_string)
    redis_client.save()
    try:
        os.remove(path / (client_uuid + ".json"))
    except Exception as e:
        print("Could not delete the configuration file for the specified user:")
        print(e)

def reset_client_usage(args: argparse.Namespace) -> None:
    """
    Reset the usage of the client(s) specified in the arguments
    """
    if args.all or args.a:
        try:
            print("All users data usages will be purged.\n\nAre you sure? (y, n): ")
            if input()[0] != 'y':
                return
            print("Reset confirmed.")

            members = redis_client.smembers(USERS_SET)
            for m in members:
                client_uuid = m.split("::")[0]
                print("Client UUID:\t\t" + client_uuid)
                print("Client Data Usage:\t\t" + str(round(get_client_usage(client_uuid, reset=True) / 1e6)) + " MB")
                print("--------------------")
        except Exception as e:
            print("ERORR: " + e)
            print("Opertation terminated.")
        else:
            print("\nAll users usage have been reset successfully.")
    else:
        for client_uuid in filter(is_valid_uuid_v4, args.reset):
            print("Client UUID:\t\t" + client_uuid)
            print("Client Data Usage:\t\t" + str(round(get_client_usage(client_uuid, reset=True) / 1e6)) + " MB")
            print("--------------------")
        print("\nAll these users usage have been reset successfully.")

def show_client_usage(args: argparse.Namespace) -> None:
    """
    Show the usage of the client(s) specified in the arguments in descending order
    """
    if args.all or args.a:
        try:
            members = redis_client.smembers(USERS_SET)
            usage_list = sorted([(get_client_usage(m.split("::")[0]), m.split("::")[0]) for m in members], reverse=True)
            print("All users data usages in descending order:\n\n")
            for usage, client_uuid in usage_list:
                print(f"{client_uuid}:\t\t{str(round(usage / 1e6))} MB")
                print("--------------------")
        except Exception as e:
            print("ERORR: " + e)
            print("Opertation terminated.")
    else:
        clients = filter(is_valid_uuid_v4, args.show_usage)
        usage_list = sorted([(get_client_usage(client), client) for client in clients], reverse=True)
        for usage, client_uuid in usage_list:
            print(f"{client_uuid}:\t\t{str(round(usage / 1e6))} MB")
            print("--------------------")

def main() -> None:
    global redis_client
    dotenv.load_dotenv()
    args = Options.args

    if args.reset:
        reset_client_usage(args)
        return

    host_addr = get_variable("host_addr", args, strict=True)
    config_path = get_variable(
        "config_path", args, strict=True, default=DEFAULT_CONFIG_PATH
    )
    redis_url = get_variable("redis_url", args, strict=True)

    config_path = pathlib.Path(config_path)
    config_path.mkdir(parents=True, exist_ok=True)

    redis_client = redis.from_url(redis_url, decode_responses=True)
    if not redis_client.ping():
        raise RuntimeError("Cannot ping redis")

    if len(args.add_with_uuid) > 0:
        print(
            get_client_addr(host_addr, generate(config_path, args, args.add_with_uuid))
        )
        return

    for _ in range(args.add):
        print(get_client_addr(host_addr, generate(config_path, args)))

    if len(args.rem) > 0:
        remove_client(config_path, args.rem)

    if args.list or args.l:
        members = redis_client.smembers(USERS_SET)
        for m in members:
            client_uuid = m.split("::")[0]
            print("Client UUID:\t\t" + client_uuid)
            print("Config Address:\t\t" + get_client_addr(host_addr, client_uuid))
            print("Client String:\t\t" + m)
            print("Client Data Usage:\t\t" + str(round(get_client_usage(client_uuid) / 1e6)) + " MB")
            print("--------------------")

    if args.show_usage:
        show_client_usage(args)


if __name__ == "__main__":
    main()
