"""Helpers shared by the FABRIC deployment notebooks."""

import datetime
import posixpath
import shlex

from deployment_config import (
    CONTROL_NODE,
    IMAGE,
    LEASE_DAYS,
    NODES,
    REMOTE_TOOLS_DIR,
    SLICE_NAME,
)

_RC_MARKER = "__FABRIC_DEPLOYMENT_RC__="


def network_name(site):
    return f"Network-{site}"


def sites():
    return sorted({node["site"] for node in NODES})


def run(node, command, check=True, quiet=False):
    """Run a bash command on a node; raise if it exits non-zero (when check=True)."""
    wrapped = f"bash -c {shlex.quote(command)}; echo {_RC_MARKER}$?"
    stdout, stderr = node.execute(wrapped, quiet=quiet)
    lines = stdout.rstrip("\n").split("\n")
    rc = None
    if lines and lines[-1].startswith(_RC_MARKER):
        rc = int(lines.pop()[len(_RC_MARKER):])
    output = "\n".join(lines)
    if check and rc != 0:
        tail = "\n".join(lines[-40:])
        raise RuntimeError(
            f"Command failed on {node.get_name()} (exit code {rc}):\n{command}\n"
            f"--- last output ---\n{tail}\n--- stderr ---\n{stderr}"
        )
    return output


def upload_script(node, local_path):
    """Upload a script into the remote tools directory, normalise line endings and make it executable."""
    remote_path = posixpath.join(REMOTE_TOOLS_DIR, posixpath.basename(local_path))
    run(node, f"mkdir -p {REMOTE_TOOLS_DIR}", quiet=True)
    node.upload_file(local_file_path=local_path, remote_file_path=remote_path)
    run(node, f"sed -i 's/\\r$//' {remote_path} && chmod +x {remote_path}", quiet=True)
    return remote_path


def run_script(node, local_path, args="", check=True, quiet=False):
    remote_path = upload_script(node, local_path)
    return run(node, f"bash {remote_path} {args}", check=check, quiet=quiet)


def get_slice(fablib):
    return fablib.get_slice(name=SLICE_NAME)


def nodes_by_name(slice_):
    return {node.get_name(): node for node in slice_.get_nodes()}


def control_node(slice_):
    return slice_.get_node(name=CONTROL_NODE)


def data_ip(node):
    return str(node.get_interface(network_name=network_name(node.get_site())).get_ip_addr())


def create_slice(fablib):
    """Build and submit the slice described in deployment_config.NODES."""
    slice_ = fablib.new_slice(name=SLICE_NAME)
    interfaces = {site: [] for site in sites()}

    for spec in NODES:
        kwargs = dict(
            name=spec["name"], site=spec["site"], cores=spec["cores"], ram=spec["ram"],
            disk=spec["disk"], image=IMAGE, validate=True, raise_exception=True,
        )
        if spec.get("host"):
            kwargs["host"] = spec["host"]
        node = slice_.add_node(**kwargs)
        interfaces[spec["site"]].append(node.add_component(model="NIC_Basic", name="NIC").get_interfaces()[0])

    for site, site_interfaces in interfaces.items():
        slice_.add_l3network(name=network_name(site), interfaces=site_interfaces, type="IPv4")

    lease_end = datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(days=LEASE_DAYS)
    slice_.submit(wait=True, wait_timeout=3600, wait_interval=20, progress=True,
                  wait_jupyter="text", lease_end_time=lease_end)
    return slice_


def configure_networking(slice_):
    """Assign data-plane IPs and add routes between the per-site FABNetv4 networks.

    Same logic as the original install_dynamos.ipynb. Addresses are handed out in the same
    order on every run, so rerunning (e.g. after a reboot) reapplies the same IPs; FABlib
    ignores "already exists" errors. Addresses are not persistent across reboots.
    """
    nodes = slice_.get_nodes()
    networks = [slice_.get_network(name=network_name(site)) for site in sites()]

    for site, network in zip(sites(), networks):
        site_nodes = [node for node in nodes if node.get_site() == site]
        available_ips = network.get_available_ips(len(site_nodes))
        network_gateway = network.get_gateway()
        network_subnet = network.get_subnet()

        for node in site_nodes:
            interface = node.get_interface(network_name=network_name(site))
            address = available_ips.pop(0)
            network.allocate_ip(address)
            interface.ip_addr_add(addr=address, subnet=network_subnet)
            node.ip_route_add(subnet=network_subnet, gateway=network_gateway)
            # Multisite IPv4 connectivity: route every site's subnet via the local gateway.
            for other in networks:
                node.ip_route_add(subnet=other.get_subnet(), gateway=network_gateway)
            print(f"{node.get_name():<12} {site:<5} {address}")


def kubespray_inventory(slice_):
    """Kubespray inventory: control plane on 'control', every other VM as a worker."""
    nodes = nodes_by_name(slice_)
    control_ip = data_ip(nodes[CONTROL_NODE])
    lines = [
        "[kube_control_plane]",
        f"{CONTROL_NODE} ansible_host={control_ip} ip={control_ip} etcd_member_name=etcd1",
        "",
        "[etcd:children]",
        "kube_control_plane",
        "",
        "[kube_node]",
    ]
    for spec in NODES:
        if spec["name"] != CONTROL_NODE:
            ip = data_ip(nodes[spec["name"]])
            lines.append(f"{spec['name']} ansible_host={ip} ip={ip}")
    return "\n".join(lines) + "\n"


def print_ssh_commands(slice_):
    for node in slice_.get_nodes():
        print(f"{node.get_name()}: {node.get_ssh_command()}")
