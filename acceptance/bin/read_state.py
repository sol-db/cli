#!/usr/bin/env python3
"""
Print selected attributes from terraform state.

Usage: <section> <name> [attr...]
"""

import sys
import os
import json


def print_resource_terraform(section, name, *attrs):
    resource_type = "databricks_" + section[:-1]
    filename = ".databricks/bundle/default/terraform/terraform.tfstate"
    raw = open(filename).read()
    data = json.loads(raw)
    found = 0
    for r in data["resources"]:
        r_type = r["type"]
        r_name = r["name"]
        if r_type != resource_type:
            continue
        if r_name != name:
            continue
        for inst in r["instances"]:
            attribute_values = inst.get("attributes")
            if attribute_values:
                values = [f"{x}={attribute_values.get(x)!r}" for x in attrs]
                print(section, name, " ".join(values))
                found += 1
    if not found:
        print(f"State not found for {section}.{name}")


def print_resource_terranova(section, name, *attrs):
    filename = ".databricks/bundle/default/resources.json"
    raw = open(filename).read()
    data = json.loads(raw)
    resources = data["resources"].get(section, {})
    result = resources.get(name)
    if result is None:
        print(f"State not found for {section}.{name}")
        return
    state = result["state"]
    state.setdefault("id", result.get("__id__"))
    values = [f"{x}={state.get(x)!r}" for x in attrs]
    print(section, name, " ".join(values))


if os.environ.get("DATABRICKS_CLI_DEPLOYMENT", "").startswith("direct"):
    print_resource_terranova(*sys.argv[1:])
else:
    print_resource_terraform(*sys.argv[1:])
