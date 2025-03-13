1. Configure network. Ubuntu 24 netplan example (all nodes):
 
```yaml

network:
  version: 2
  renderer: networkd
  ethernets:
    enp0s31f6:
      addresses:
        - 22.22.22.2/24
      routes:
        - to: 0.0.0.0/0
          via: 22.22.22.1
          on-link: true
          metric: 100
      routing-policy:
        - from: 22.22.22.1
          table: 100
      nameservers:
        addresses:
          - 8.8.8.8
          - 8.8.4.4
  vlans:
    enp0s31f6.4012:
      id: 4012
      link: enp0s31f6
      mtu: 1460
      addresses:
        - 192.168.100.2/22
```

2. Update, upgrade, install soft:

```bash
sudo apt -y update
sudo apt -y upgrade
sudo apt -y install unzip iptables
```

2. Disable IPv6 and increase inotify limits. Add to `/etc/sysctl.conf` (all nodes, optional)

```bash
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1
net.ipv6.conf.lo.disable_ipv6 = 1
fs.inotify.max_user_watches=1524288
fs.inotify.max_user_instances=4024
```

and run 

```bash 
sudo sysctl -p
```

3. Disable `resolved`: 

```bash
systemctl stop systemd-resolved
systemctl disable systemd-resolved
rm /etc/resolv.conf
echo "nameserver 8.8.8.8" > /etc/resolv.conf
echo "nameserver 8.8.4.4" >> /etc/resolv.conf
```

4. Edit `/etc/hosts` and remove all ipv6:

```bash
127.0.0.1 localhost.localdomain localhost
22.22.22.3 kube-dc-master-1
```

3. Clone git repo (initial master node): 

```bash 
git clone https://github.com/shalb/kube-dc.git
cd kube-dc
```

4. Check passwordless connection to all other nodes.

```bash
ssh root@22.22.22.3
```

7. Install `cluster.dev`. https://docs.cluster.dev/installation-upgrade/

```bash
curl -fsSL https://raw.githubusercontent.com/shalb/cluster.dev/master/scripts/get_cdev.sh | sh
```

8. Configure and install rke2 cluster initial node. 
  8.1 Install `kubectl`
```bash


```

  8.2 Create rke2 config file `/etc/rancher/rke2/config.yaml`:
```bash
# run from root or sudo -s
mkdir -p /etc/rancher/rke2/

cat <<EOF > /etc/rancher/rke2/config.yaml
node-name: kube-dc-master-1
disable-cloud-controller: true
disable: rke2-ingress-nginx
cni: none
cluster-cidr: "10.100.0.0/16"
service-cidr: "10.101.0.0/16"
cluster-dns: "10.101.0.11"
node-label:
  - kube-dc-manager=true
  - kube-ovn/role=master
kube-apiserver-arg: 
  - authentication-config=/etc/rancher/auth-conf.yaml
debug: true
node-external-ip: 138.201.253.201
tls-san:
  - kube-api.dev.kube-dc.com
  - 138.201.253.201
advertise-address: 138.201.253.201
node-ip: 192.168.100.2
EOF
```

  8.2 Create kubernetes auth file:
```bash
# run from root or sudo -s
cat <<EOF > /etc/rancher/auth-conf.yaml
apiVersion: apiserver.config.k8s.io/v1beta1
kind: AuthenticationConfiguration
jwt: []
EOF
chmod 666 /etc/rancher/auth-conf.yaml
```
  8.3 Install rke2: 

```bash
# run from root or sudo -s
export INSTALL_RKE2_VERSION="v1.32.1+rke2r1" # https://docs.rke2.io/release-notes/v1.32.X (required kubernetes v1.31 or later)
export INSTALL_RKE2_TYPE="server"

curl -sfL https://get.rke2.io | sh -

systemctl enable rke2-server.service
systemctl start rke2-server.service

journalctl -u rke2-server -f
```

  8.4 Get kubeconfig and check cluster access
```bash
# run from your user
sudo cp /etc/rancher/rke2/rke2.yaml ~/.kube/config
sudo chown "$(whoami):$(whoami)" ~/.kube/config

kubectl get node
# kube-dc-master-1   NotReady   control-plane,etcd,master   10m   v1.32.1+rke2r1
# NotReady node it's ok
```

9. Configure and join master node.
  9.1 Get join token (on init master node):
```bash
sudo cat /var/lib/rancher/rke2/server/node-token
```
  9.2 Create rke2 config file `/etc/rancher/rke2/config.yaml`:
```bash
# run from root or sudo -s
mkdir -p /etc/rancher/rke2/

cat <<EOF > /etc/rancher/rke2/config.yaml
token: <TOKEN>
server: https://138.201.253.201:9345
node-name: kube-dc-master-2
disable-cloud-controller: true
disable: rke2-ingress-nginx
cluster-cidr: "10.100.0.0/16"
service-cidr: "10.101.0.0/16"
cluster-dns: "10.101.0.11"
node-label:
  - kube-ovn/role=master
debug: true
node-external-ip: 88.99.29.250
tls-san:
  - kube-api.dev.kube-dc.com
  - 88.99.29.250
advertise-address: 88.99.29.250
node-ip: 192.168.100.3
EOF
```

  8.3 Install rke2: 

```bash
# run from root or sudo -s
export INSTALL_RKE2_VERSION="v1.32.1+rke2r1" # https://docs.rke2.io/release-notes/v1.32.X (required kubernetes v1.31 or later)
export INSTALL_RKE2_TYPE="server"

curl -sfL https://get.rke2.io | sh -

systemctl enable rke2-server.service
systemctl start rke2-server.service

journalctl -u rke2-server -f
```

9. Configure and join worker node.
  9.1 Get join token (on init master node):
```bash
sudo cat /var/lib/rancher/rke2/server/node-token
```
  9.2 Create rke2 config file:
```bash
# run from root or sudo -s
mkdir -p /etc/rancher/rke2/

cat <<EOF > /etc/rancher/rke2/config.yaml
token: <TOKEN>
server: https://192.168.100.2:9345
node-name: kube-dc-worker-1
node-ip: 192.168.100.3
EOF
```

  8.3 Install rke2: 

```bash
# run from root or sudo -s
export INSTALL_RKE2_VERSION="v1.32.1+rke2r1" # https://docs.rke2.io/release-notes/v1.32.X (required kubernetes v1.31 or later)
export INSTALL_RKE2_TYPE="agent"

curl -sfL https://get.rke2.io | sh -

systemctl enable rke2-agent.service
systemctl start rke2-agent.service

journalctl -u rke2-agent -f
```

9. Install kube-dc stack

In installer folder `inatsller/kube-dc/`, edit stack.yaml like this:

```yaml
name: cluster
template: "./templates/kube-dc/"
kind: Stack
backend: default
variables:
  debug: "true"
  kubeconfig: /home/arti/.kube/config

  monitoring:
    prom_storage: 20Gi
    retention_size: 17GiB
    retention: 365d
  
  cluster_config:
    pod_cidr: "10.100.0.0/16"
    svc_cidr: "10.101.0.0/16"
    join_cidr: "100.64.0.0/16"
    cluster_dns: "10.101.0.11"
    default_external_network:
      nodes_list: # list of nodes, where 4011 vlan is accessible
        - kube-dc-master-1
        - kube-dc-worker-1
      name: external4011
      vlan_id: "4011"
      interface: "enp0s31f6"
      cidr: "167.235.85.112/29"
      gateway: 167.235.85.113
      mtu: "1400"
    
  node_external_ip: 138.201.253.201 # wildcard *.dev.kube-dc.com shoud be faced on this ip

  email: "noreply@shalb.com"
  domain: "dev.kube-dc.com"
  install_terraform: true

  create_default:
    organization:
      name: shalb
      description: "My test org my-org 1"
      email: "arti@shalb.com"
    project:
      name: demo
      cidr_block: "10.1.0.0/16"
   

  versions:
    kube_dc: "v0.1.20" # release version
    rke2: "v1.32.1+rke2r1"
```
install: 

```bash
cdev apply
```
