#!/usr/bin/env python3
"""
Script to generate OS test VMs and DataVolumes from ConfigMap
Usage: python3 generate-os-test-vms.py [namespace] [action]
Actions: create, delete, status
"""

import sys
import subprocess
import yaml
import json
import re
from typing import Dict, List, Any

def run_command(cmd: List[str], capture_output: bool = True) -> subprocess.CompletedProcess:
    """Run a command and return the result"""
    try:
        return subprocess.run(cmd, capture_output=capture_output, text=True, check=True)
    except subprocess.CalledProcessError as e:
        print(f"❌ Command failed: {' '.join(cmd)}")
        print(f"Error: {e.stderr}")
        sys.exit(1)

def log(message: str):
    """Log a message with timestamp"""
    import datetime
    timestamp = datetime.datetime.now().strftime("%H:%M:%S")
    print(f"\033[0;34m[{timestamp}]\033[0m {message}")

def success(message: str):
    """Print success message"""
    print(f"\033[0;32m✅ {message}\033[0m")

def warning(message: str):
    """Print warning message"""
    print(f"\033[1;33m⚠️  {message}\033[0m")

def error(message: str):
    """Print error message"""
    print(f"\033[0;31m❌ {message}\033[0m")

def check_namespace(namespace: str):
    """Check if namespace exists"""
    try:
        run_command(["kubectl", "get", "namespace", namespace])
    except:
        error(f"Namespace '{namespace}' does not exist!")
        sys.exit(1)

def get_configmap_data(configmap_file: str) -> Dict[str, Any]:
    """Extract OS images from ConfigMap template file"""
    log(f"Parsing OS images from ConfigMap file: {configmap_file}")
    
    try:
        with open(configmap_file, 'r') as f:
            content = f.read()
        
        # Extract the images.yaml section manually since it's a Helm template
        # Find the images.yaml: | line and extract everything until the end
        lines = content.split('\n')
        os_images_lines = []
        in_images = False
        
        for line in lines:
            if 'images.yaml:' in line and '|' in line:
                in_images = True
                continue
            elif in_images:
                # Stop when we hit another top-level key or end of file
                if line and not line.startswith('  ') and not line.startswith('\t'):
                    break
                os_images_lines.append(line)
        
        # Remove common indentation and parse
        if os_images_lines:
            # Remove empty lines at the end
            while os_images_lines and not os_images_lines[-1].strip():
                os_images_lines.pop()
            
            # Find minimum indentation (excluding empty lines)
            non_empty_lines = [line for line in os_images_lines if line.strip()]
            if non_empty_lines:
                min_indent = min(len(line) - len(line.lstrip()) for line in non_empty_lines)
                os_images_lines = [line[min_indent:] if len(line) > min_indent else line for line in os_images_lines]
        
        # Join and parse as YAML
        os_images_content = '\n'.join(os_images_lines)
        parsed_data = yaml.safe_load(os_images_content)
        
        if not parsed_data or 'images' not in parsed_data:
            error("No images found in ConfigMap")
            sys.exit(1)
            
        return parsed_data['images']
    except Exception as e:
        error(f"Failed to parse ConfigMap file: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(1)

def sanitize_name(name: str) -> str:
    """Convert OS name to valid Kubernetes resource name"""
    # Convert to lowercase, replace spaces and special chars with hyphens
    sanitized = re.sub(r'[^a-z0-9]+', '-', name.lower())
    # Remove leading/trailing hyphens and collapse multiple hyphens
    sanitized = re.sub(r'^-+|-+$', '', sanitized)
    sanitized = re.sub(r'-+', '-', sanitized)
    return f"test-{sanitized}"

def generate_datavolume(os_config: Dict[str, Any], namespace: str) -> Dict[str, Any]:
    """Generate DataVolume YAML"""
    vm_name = sanitize_name(os_config['OS_NAME'])
    
    return {
        'apiVersion': 'cdi.kubevirt.io/v1beta1',
        'kind': 'DataVolume',
        'metadata': {
            'name': f"{vm_name}-root",
            'namespace': namespace,
            'labels': {
                'app.kubernetes.io/created-by': 'os-test-script',
                'os-test/os-name': sanitize_name(os_config['OS_NAME']).replace('test-', '')
            }
        },
        'spec': {
            'pvc': {
                'accessModes': ['ReadWriteOnce'],
                'resources': {
                    'requests': {
                        'storage': os_config['MIN_STORAGE']
                    }
                },
                'storageClassName': 'local-path'
            },
            'source': {
                'http': {
                    'url': os_config['OS_IMAGE_URL']
                }
            }
        }
    }

def generate_vm(os_config: Dict[str, Any], namespace: str) -> Dict[str, Any]:
    """Generate VirtualMachine YAML"""
    vm_name = sanitize_name(os_config['OS_NAME'])
    
    # Skip CirrOS for now (minimal test image)
    if 'CirrOS' in os_config['OS_NAME']:
        return None
    
    # Check if this is a Windows VM
    is_windows = 'Windows' in os_config['OS_NAME']
    
    # Base VM spec
    vm_spec = {
        'apiVersion': 'kubevirt.io/v1',
        'kind': 'VirtualMachine',
        'metadata': {
            'name': vm_name,
            'namespace': namespace,
            'labels': {
                'app.kubernetes.io/created-by': 'os-test-script',
                'os-test/os-name': sanitize_name(os_config['OS_NAME']).replace('test-', '')
            }
        },
        'spec': {
            'running': True,
            'template': {
                'spec': {
                    'networks': [{
                        'name': 'vpc_net_0',
                        'multus': {
                            'default': True,
                            'networkName': f"{namespace}/default"
                        }
                    }],
                    'domain': {
                        'devices': {
                            'interfaces': [{
                                'name': 'vpc_net_0',
                                'bridge': {}
                            }],
                            'disks': [
                                {
                                    'disk': {'bus': 'virtio'},
                                    'name': 'root-volume'
                                },
                                {
                                    'name': 'cloudinitdisk',
                                    'disk': {'bus': 'virtio'}
                                }
                            ]
                        },
                        'machine': {'type': os_config.get('MACHINE_TYPE', 'q35')},
                        'cpu': {'cores': int(os_config['MIN_VCPU'])},
                        'memory': {'guest': os_config['MIN_MEMORY']}
                    },
                    'readinessProbe': {
                        'guestAgentPing': {},
                        'failureThreshold': 10,
                        'initialDelaySeconds': 30,
                        'periodSeconds': 10,
                        'timeoutSeconds': 5
                    },
                    'accessCredentials': [{
                        'sshPublicKey': {
                            'source': {
                                'secret': {'secretName': 'authorized-keys-default'}
                            },
                            'propagationMethod': {
                                'qemuGuestAgent': {
                                    'users': [os_config['CLOUD_USER']]
                                }
                            }
                        }
                    }] if not is_windows else [],
                    'terminationGracePeriodSeconds': 60,
                    'volumes': [
                        {
                            'dataVolume': {'name': f"{vm_name}-root"},
                            'name': 'root-volume'
                        },
                        {
                            'name': 'cloudinitdisk',
                            'cloudInitNoCloud': {
                                'userData': os_config['CLOUD_INIT']
                            }
                        }
                    ]
                }
            }
        }
    }
    
    # Add EFI firmware if specified
    if os_config.get('FIRMWARE_TYPE') == 'efi':
        vm_spec['spec']['template']['spec']['domain']['firmware'] = {
            'bootloader': {
                'efi': {
                    'persistent': True,
                    'secureBoot': False
                }
            }
        }
    
    return vm_spec

def get_existing_resources(namespace: str) -> Dict[str, Dict]:
    """Get existing test resources"""
    existing = {'datavolumes': {}, 'vms': {}}
    
    try:
        # Get existing DataVolumes
        result = run_command(["kubectl", "get", "dv", "-n", namespace, "-l", "app.kubernetes.io/created-by=os-test-script", "-o", "json"])
        dv_data = json.loads(result.stdout)
        for item in dv_data.get('items', []):
            existing['datavolumes'][item['metadata']['name']] = item
    except:
        pass
    
    try:
        # Get existing VMs
        result = run_command(["kubectl", "get", "vm", "-n", namespace, "-l", "app.kubernetes.io/created-by=os-test-script", "-o", "json"])
        vm_data = json.loads(result.stdout)
        for item in vm_data.get('items', []):
            existing['vms'][item['metadata']['name']] = item
    except:
        pass
    
    return existing

def resource_needs_update(existing_resource: Dict, new_resource: Dict) -> bool:
    """Check if resource needs update based on key fields"""
    if not existing_resource:
        return True
    
    # Check if OS name label has changed
    existing_os = existing_resource.get('metadata', {}).get('labels', {}).get('os-test/os-name', '')
    new_os = new_resource.get('metadata', {}).get('labels', {}).get('os-test/os-name', '')
    
    if existing_os != new_os:
        return True
    
    # For DataVolumes, check storage size and image URL
    if new_resource.get('kind') == 'DataVolume':
        existing_storage = existing_resource.get('spec', {}).get('pvc', {}).get('resources', {}).get('requests', {}).get('storage', '')
        new_storage = new_resource.get('spec', {}).get('pvc', {}).get('resources', {}).get('requests', {}).get('storage', '')
        
        existing_url = existing_resource.get('spec', {}).get('source', {}).get('http', {}).get('url', '')
        new_url = new_resource.get('spec', {}).get('source', {}).get('http', {}).get('url', '')
        
        if existing_storage != new_storage or existing_url != new_url:
            return True
    
    # For VMs, check memory, CPU, and cloud-init
    if new_resource.get('kind') == 'VirtualMachine':
        existing_memory = existing_resource.get('spec', {}).get('template', {}).get('spec', {}).get('domain', {}).get('memory', {}).get('guest', '')
        new_memory = new_resource.get('spec', {}).get('template', {}).get('spec', {}).get('domain', {}).get('memory', {}).get('guest', '')
        
        existing_cpu = existing_resource.get('spec', {}).get('template', {}).get('spec', {}).get('domain', {}).get('cpu', {}).get('cores', 0)
        new_cpu = new_resource.get('spec', {}).get('template', {}).get('spec', {}).get('domain', {}).get('cpu', {}).get('cores', 0)
        
        if existing_memory != new_memory or existing_cpu != new_cpu:
            return True
    
    return False

def create_resources(namespace: str, configmap_file: str, os_filter: str = None):
    """Create or update VMs and DataVolumes (idempotent)"""
    if os_filter:
        log(f"Creating/updating OS test resources for '{os_filter}' in namespace '{namespace}'...")
    else:
        log(f"Creating/updating OS test resources in namespace '{namespace}'...")
    
    os_images = get_configmap_data(configmap_file)
    
    # Filter OS images if specified
    if os_filter:
        filtered_images = []
        for os_config in os_images:
            if os_filter.lower() in os_config['OS_NAME'].lower():
                filtered_images.append(os_config)
        
        if not filtered_images:
            error(f"No OS images found matching '{os_filter}'")
            print("Available OS images:")
            for os_config in os_images:
                print(f"  - {os_config['OS_NAME']}")
            sys.exit(1)
        
        os_images = filtered_images
        log(f"Found {len(os_images)} matching OS image(s)")
    
    existing = get_existing_resources(namespace)
    
    created_dvs = 0
    updated_dvs = 0
    created_vms = 0
    updated_vms = 0
    
    for os_config in os_images:
        vm_name = sanitize_name(os_config['OS_NAME'])
        dv_name = f"{vm_name}-root"
        
        # Generate DataVolume
        dv = generate_datavolume(os_config, namespace)
        existing_dv = existing['datavolumes'].get(dv_name)
        
        if resource_needs_update(existing_dv, dv):
            action = "Updating" if existing_dv else "Creating"
            log(f"{action} DataVolume {dv_name}...")
            
            dv_yaml = yaml.dump(dv, default_flow_style=False)
            proc = subprocess.run(['kubectl', 'apply', '-f', '-'], input=dv_yaml, text=True)
            if proc.returncode == 0:
                if existing_dv:
                    updated_dvs += 1
                else:
                    created_dvs += 1
            else:
                error(f"Failed to apply DataVolume {dv_name}")
        
        # Generate VM (skip CirrOS)
        vm = generate_vm(os_config, namespace)
        if vm:
            existing_vm = existing['vms'].get(vm_name)
            
            if resource_needs_update(existing_vm, vm):
                action = "Updating" if existing_vm else "Creating"
                log(f"{action} VM {vm_name}...")
                
                vm_yaml = yaml.dump(vm, default_flow_style=False)
                proc = subprocess.run(['kubectl', 'apply', '-f', '-'], input=vm_yaml, text=True)
                if proc.returncode == 0:
                    if existing_vm:
                        updated_vms += 1
                    else:
                        created_vms += 1
                else:
                    error(f"Failed to apply VM {vm_name}")
    
    success("Resources synchronized successfully!")
    print(f"DataVolumes: {created_dvs} created, {updated_dvs} updated")
    print(f"VMs: {created_vms} created, {updated_vms} updated")

def delete_resources(namespace: str, os_filter: str = None):
    """Delete VMs and DataVolumes"""
    if os_filter:
        log(f"Deleting OS test resources for '{os_filter}' in namespace '{namespace}'...")
        
        # Get matching VMs and DataVolumes
        existing = get_existing_resources(namespace)
        deleted_vms = 0
        deleted_dvs = 0
        
        for vm_name, vm_data in existing['vms'].items():
            os_label = vm_data.get('metadata', {}).get('labels', {}).get('os-test/os-name', '')
            if os_filter.lower() in os_label.lower() or os_filter.lower() in vm_name.lower():
                try:
                    run_command(["kubectl", "delete", "vm", vm_name, "-n", namespace, "--ignore-not-found=true"])
                    deleted_vms += 1
                    log(f"Deleted VM: {vm_name}")
                except:
                    pass
        
        for dv_name, dv_data in existing['datavolumes'].items():
            os_label = dv_data.get('metadata', {}).get('labels', {}).get('os-test/os-name', '')
            if os_filter.lower() in os_label.lower() or os_filter.lower() in dv_name.lower():
                try:
                    run_command(["kubectl", "delete", "dv", dv_name, "-n", namespace, "--ignore-not-found=true"])
                    deleted_dvs += 1
                    log(f"Deleted DataVolume: {dv_name}")
                except:
                    pass
        
        success(f"Deleted {deleted_vms} VMs and {deleted_dvs} DataVolumes matching '{os_filter}'")
    else:
        log(f"Deleting all OS test resources in namespace '{namespace}'...")
        
        # Delete VMs with our label
        try:
            run_command(["kubectl", "delete", "vm", "-n", namespace, "-l", "app.kubernetes.io/created-by=os-test-script", "--ignore-not-found=true"])
        except:
            pass
        
        # Delete DataVolumes with our label
        try:
            run_command(["kubectl", "delete", "dv", "-n", namespace, "-l", "app.kubernetes.io/created-by=os-test-script", "--ignore-not-found=true"])
        except:
            pass
        
        success("All resources deleted successfully!")

def show_status(namespace: str):
    """Show status of test resources"""
    log(f"OS Test Resources Status in namespace '{namespace}':")
    
    print("\n=== DataVolumes ===")
    try:
        result = run_command(["kubectl", "get", "dv", "-n", namespace, "-l", "app.kubernetes.io/created-by=os-test-script"])
        print(result.stdout)
    except:
        print("No test DataVolumes found")
    
    print("\n=== VirtualMachines ===")
    try:
        result = run_command(["kubectl", "get", "vm", "-n", namespace, "-l", "app.kubernetes.io/created-by=os-test-script"])
        print(result.stdout)
    except:
        print("No test VMs found")
    
    print("\n=== Guest Agent Status ===")
    try:
        result = run_command(["kubectl", "get", "vm", "-n", namespace, "-l", "app.kubernetes.io/created-by=os-test-script", "-o", "jsonpath={.items[*].metadata.name}"])
        vm_names = result.stdout.strip().split()
        
        for vm_name in vm_names:
            if not vm_name:
                continue
                
            try:
                # Check if VMI exists
                vmi_result = run_command(["kubectl", "get", "vmi", vm_name, "-n", namespace, "-o", "json"])
                vmi_data = json.loads(vmi_result.stdout)
                
                phase = vmi_data.get('status', {}).get('phase', 'Unknown')
                conditions = vmi_data.get('status', {}).get('conditions', [])
                
                agent_connected = "N/A"
                ssh_keys_synced = "N/A"
                
                ssh_error_message = ""
                for condition in conditions:
                    if condition.get('type') == 'AgentConnected':
                        agent_connected = condition.get('status', 'N/A')
                    elif condition.get('type') == 'AccessCredentialsSynchronized':
                        ssh_keys_synced = condition.get('status', 'N/A')
                        if ssh_keys_synced == "False":
                            ssh_error_message = condition.get('message', '')
                
                if agent_connected == "True" and ssh_keys_synced == "True":
                    print(f"🐧 {vm_name}: \033[0;32m✅ SUCCESS\033[0m (Phase: {phase}, Agent: {agent_connected}, SSH: {ssh_keys_synced})")
                elif phase == "Running":
                    error_details = f"Agent: {agent_connected}, SSH: {ssh_keys_synced}"
                    if ssh_error_message and "SELinux" in ssh_error_message or "failed to lookup user" in ssh_error_message:
                        error_details += " [SELinux Issue]"
                    print(f"🐧 {vm_name}: \033[0;31m❌ FAILED\033[0m (Phase: {phase}, {error_details})")
                else:
                    print(f"🐧 {vm_name}: \033[1;33m⏳ PENDING\033[0m (Phase: {phase})")
                    
            except:
                # VMI doesn't exist, check VM status
                try:
                    vm_result = run_command(["kubectl", "get", "vm", vm_name, "-n", namespace, "-o", "jsonpath={.status.printableStatus}"])
                    vm_status = vm_result.stdout.strip()
                    print(f"🐧 {vm_name}: \033[1;33m⏳ PENDING\033[0m (Status: {vm_status})")
                except:
                    print(f"🐧 {vm_name}: \033[0;31m❌ ERROR\033[0m (Cannot get status)")
                    
    except:
        print("No test VMs found")

def list_available_os():
    """List available OS images from ConfigMap"""
    import os
    script_dir = os.path.dirname(os.path.abspath(__file__))
    configmap_file = os.path.join(script_dir, "../../charts/kube-dc/templates/os-images-configmap.yaml")
    
    try:
        os_images = get_configmap_data(configmap_file)
        print("Available OS Images:")
        for i, os_config in enumerate(os_images, 1):
            print(f"  {i:2d}. {os_config['OS_NAME']}")
            print(f"      User: {os_config['CLOUD_USER']}")
            print(f"      Resources: {os_config['MIN_MEMORY']} RAM, {os_config['MIN_VCPU']} vCPU, {os_config['MIN_STORAGE']} storage")
            print()
    except Exception as e:
        error(f"Failed to list OS images: {e}")
        sys.exit(1)

def main():
    """Main function"""
    if len(sys.argv) < 2:
        print("Usage: python3 generate-os-test-vms.py [namespace] <action> [os-filter]")
        print("Actions:")
        print("  create [os-filter]  - Create/update VMs (optionally filter by OS name)")
        print("  delete [os-filter]  - Delete VMs (optionally filter by OS name)")
        print("  status              - Show status of test resources")
        print("  list                - List available OS images from ConfigMap")
        print()
        print("Examples:")
        print("  python3 generate-os-test-vms.py shalb-dev create")
        print("  python3 generate-os-test-vms.py shalb-dev create fedora")
        print("  python3 generate-os-test-vms.py shalb-dev delete ubuntu")
        print("  python3 generate-os-test-vms.py shalb-dev list")
        print()
        print("Default namespace: shalb-dev")
        sys.exit(1)
    
    namespace = sys.argv[1] if sys.argv[1] != "list" else "shalb-dev"
    action = sys.argv[2] if len(sys.argv) > 2 and sys.argv[1] != "list" else (sys.argv[1] if sys.argv[1] == "list" else "create")
    os_filter = sys.argv[3] if len(sys.argv) > 3 else None
    
    # Handle list action specially
    if action == "list" or sys.argv[1] == "list":
        list_available_os()
        return
    
    # Path to ConfigMap template file
    import os
    script_dir = os.path.dirname(os.path.abspath(__file__))
    configmap_file = os.path.join(script_dir, "../../charts/kube-dc/templates/os-images-configmap.yaml")
    
    if action == "create":
        check_namespace(namespace)
        create_resources(namespace, configmap_file, os_filter)
    elif action == "delete":
        check_namespace(namespace)
        delete_resources(namespace, os_filter)
    elif action == "status":
        check_namespace(namespace)
        show_status(namespace)
    else:
        error(f"Unknown action: {action}")
        print("Valid actions: create, delete, status, list")
        sys.exit(1)

if __name__ == "__main__":
    main()
