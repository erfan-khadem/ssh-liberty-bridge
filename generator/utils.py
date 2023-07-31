import argparse
import os
import uuid


def is_valid_uuid_v4(input_str) -> bool:
    """
    Checks if the input string is a valid UUID v4
    """
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
