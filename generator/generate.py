import os
import pathlib
import uuid
import argparse
import redis
import dotenv
import json
import glob

from string import Template

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ed25519

redis_client: redis.Redis = None
DEFAULT_CONFIG_PATH: str = "/var/www/html/ssh-server/"
USERS_SET = "ssh-server:users"


def is_valid_uuid_v4(input_str) -> bool:
    try:
        uuid_obj = uuid.UUID(input_str, version=4)
        return str(uuid_obj) == input_str
    except ValueError:
        return False


def get_variable(
    variable: str,
    args: argparse.Namespace,
    strict: bool = False,
    default: str | None = None,
) -> str | None:
    """
    Gets the requested variable from args or environmental variables

    First try to find the variable in `args`, then in the environment variables.
    If `default` is provided, in case `strict` is True, `default` is returned.
    If `strict` is False and the variable is not found, None is returned regardless of the state of default
    """
    lvar = variable.lower()
    uvar = variable.upper()
    try:
        tmp = None
        has_var = lvar in args.__dict__
        if has_var:
            tmp = args.__dict__.get(lvar)

        if (
            not has_var
            or (type(tmp) == str and len(tmp) == 0)
            or (type(tmp) == int and tmp == -1)
        ):
            result = os.getenv(uvar)
            if result is None:
                raise RuntimeError("Required variable not found: " + uvar)
            return result

        return str(tmp)
    except Exception as e:
        if not default is None:
            return default
        elif strict:
            raise e
        else:
            print(f"Returning None for {lvar}")
            return None


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Client management tool for ssh-server"
    )
    parser.add_argument(
        "--add", type=int, default=0, help="Number of new configurations to generate"
    )
    parser.add_argument(
        "--rem", type=str, default="", help="Configuration UUID to remove"
    )
    parser.add_argument("--list", default=False, action="store_true", help="List configurations")
    parser.add_argument(
        "--config-path", type=str, default="", help="Path to configuration storage area"
    )
    parser.add_argument(
        "--add-with-uuid",
        type=str,
        default="",
        help="Add one configuration with the specified uuid",
    )
    parser.add_argument(
        "--redis-url", type=str, default="", help="Redis database to connect to"
    )
    parser.add_argument(
        "--host-addr",
        type=str,
        default="",
        help="Host address to download client configs from. The client UUID replaces {uuid} part in the given string",
    )
    parser.add_argument(
        "--server-addr",
        type=str,
        default="",
        help="Server address for the clients to connect to",
    )
    parser.add_argument(
        "--server-port",
        type=int,
        default=-1,
        help="Server port for the clients to connect to. Read from environmental variables or .env by default",
    )
    parser.add_argument(
        "--host-key-path",
        type=str,
        default="",
        help="Path to server's host keys",
    )
    parser.add_argument(
        "--template-path",
        type=str,
        default="",
        help="Client configuration template file path",
    )
    return parser.parse_args()


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


def main() -> None:
    global redis_client
    dotenv.load_dotenv()
    args = parse_args()
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

    if args.list:
        members = redis_client.smembers(USERS_SET)
        for m in members:
            client_uuid = m.split("::")[0]
            print("Client UUID:\t\t" + client_uuid)
            print("Config Address:\t\t" + get_client_addr(host_addr, client_uuid))
            print("Client String:\t\t" + m)
            print("--------------------")


if __name__ == "__main__":
    main()
