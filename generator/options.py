import argparse
import redis
from typing import Optional
from generator.settings import USERS_SET, USERS_USAGE
from generator.utils import is_valid_uuid_v4

class Options:
    """
    Parses the command line arguments
    """
    def __init__(self) -> None:
        self._args: Optional[argparse.Namespace] = None

    def _parse_args(self) -> argparse.Namespace:
        parser = argparse.ArgumentParser(
            description="Client management tool for ssh-server"
        )
        parser.add_argument(
            "--all", "-a", default=False, action="store_true", help="Apply to all clients"
        )
        parser.add_argument(
            "--add", type=int, default=0, help="Number of new configurations to generate"
        )
        parser.add_argument(
            "--rem", type=str, default="", help="Configuration UUID to remove"
        )
        parser.add_argument(
            "--list", "-l", default=False, action="store_true", help="List configurations"
        )
        parser.add_argument(
            "--reset", nargs="*", action="append", help="Reset user's data usage"
        )
        parser.add_argument(
            "--show-usage", nargs="*", action="append", help="Show user's data usage"
        )
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

    @property
    def args(self) -> argparse.Namespace:
        """
        Returns the parsed arguments
        """
        if self._args is None:
            self._args = self._parse_args()
        return self._args
