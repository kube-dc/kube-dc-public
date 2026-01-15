# Kube-DC Keycloak Theme

Custom Keycloak theme matching the Kube-DC PatternFly 5 frontend application styling.

## 📁 Directory Structure

```
hack/keycloak-theme/
├── kube-dc/                                    # Theme source directory
│   ├── theme.properties                        # Root theme configuration
│   ├── common/                                 # Shared resources (single source of truth)
│   │   └── resources/
│   │       ├── favicon.png                     # Favicon (used by all themes)
│   │       ├── logo.png                        # Kube-DC logo
│   │       └── kube-dc-white-transparent.png   # Background image
│   ├── login/                                  # Login pages
│   │   ├── theme.properties                    # Login theme config
│   │   ├── login.ftl                           # Custom login template
│   │   ├── template.ftl                        # Base template
│   │   ├── messages/
│   │   │   └── messages_en.properties          # i18n messages
│   │   └── resources/css/
│   │       └── kube-dc.css                     # Login CSS styles
│   └── account/                                # Account management pages
│       ├── theme.properties                    # Account theme config
│       └── resources/css/
│           └── kube-dc-account.css             # Account CSS styles
├── build-theme.sh                              # Build JAR and generate manifests
├── set-theme.sh                                # Activate theme via Keycloak API
├── keycloak-values.yaml                        # Helm values for development
└── README.md                                   # This file
```

## 🎨 Style Guide

The theme matches the Kube-DC frontend with the following design elements:

### Colors
- **Header Background**: `#002952` (Dark blue)
- **Primary Button**: `#0066CC` (Blue)
- **Accent**: `#40199A` (Purple)
- **Text Primary**: `#151515`
- **Text Muted**: `#6a6e73`
- **Success**: `#3e8635`
- **Error**: `#c9190b`
- **Warning**: `#f0ab00`
- **Info**: `#2b9af3`

### Typography
- Base font size: 14px
- Font family: Red Hat Display, Overpass
- Button text: 16px, weight 600

### Components
- Input height: 44px
- Button height: 44px
- Border radius: 4px
- Border color: `#d2d2d2`

## 🚀 Quick Start

### Build the Theme

```bash
cd hack/keycloak-theme
./build-theme.sh
```

This will:
1. Package the theme into a JAR file
2. Generate a ConfigMap YAML with base64-encoded JAR
3. Create Helm values patch
4. Create deployment script

### Deploy to Kubernetes

```bash
cd hack/keycloak-theme/build
./deploy-theme.sh
```

Or manually:

```bash
# Apply ConfigMap
kubectl apply -f hack/keycloak-theme/build/keycloak-theme-configmap.yaml

# Update Helm values (add extraVolumes and extraVolumeMounts)
# See hack/keycloak-theme/build/helm-values-patch.yaml

# Restart Keycloak
kubectl rollout restart statefulset/keycloak -n keycloak
```

## 📝 Configuration Steps

### 1. Build and Deploy Theme

```bash
./build-theme.sh
cd build
./deploy-theme.sh
```

### 2. Update Keycloak Helm Values

Add to `installer/kube-dc/templates/kube-dc/keycloak/values.yaml`:

```yaml
extraVolumes:
  - name: kube-dc-theme
    configMap:
      name: kube-dc-theme

extraVolumeMounts:
  - name: kube-dc-theme
    mountPath: /opt/bitnami/keycloak/providers/kube-dc-theme.jar
    subPath: kube-dc-theme.jar
```

### 3. Restart Keycloak

```bash
kubectl rollout restart statefulset/keycloak -n keycloak
```

Wait for pods to be ready:

```bash
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=keycloak -n keycloak --timeout=300s
```

### 4. Activate Theme (Automated)

Run the automated theme activation script:

```bash
cd hack/keycloak-theme
./set-theme.sh
```

This script will:
- Extract credentials from `master-config` secret in `kube-dc` namespace
- Authenticate with Keycloak Admin API
- Set `kube-dc` theme for all existing realms
- Configure master realm to use theme as default for new realms

**Manual Alternative:**

If you prefer manual configuration:
1. Log into Keycloak Admin Console: `https://login.your-domain.com`
2. Navigate to **Realm Settings** for each realm
3. Go to **Themes** tab
4. Set **Login Theme**, **Account Theme**, and **Email Theme** to `kube-dc`
5. Click **Save**

### 5. Activate Theme Automatically

```bash
./set-theme.sh
```

### 6. Test the Login Page

Visit your organization login page:
```
https://console.your-domain.com
```

The login page should now match your frontend styling with:
- Dark blue header with Kube-DC logo
- White card with rounded corners
- Blue primary buttons
- Matching typography and colors

## 🔧 Customization

### Modify Colors

Edit `kube-dc/login/resources/css/kube-dc.css`:

```css
:root {
    --kube-dc-header: #002952;      /* Change header color */
    --kube-dc-primary: #0066CC;     /* Change button color */
    --kube-dc-accent: #40199A;      /* Change accent color */
}
```

### Update Logo

Replace `kube-dc/login/resources/img/logo.png` with your logo, then rebuild:

```bash
./build-theme.sh
cd build
kubectl apply -f keycloak-theme-configmap.yaml
kubectl rollout restart statefulset/keycloak -n keycloak
```

### Customize Login Template

Edit `kube-dc/login/login.ftl` to modify the HTML structure.

Available Freemarker templates in Keycloak:
- `login.ftl` - Main login page
- `register.ftl` - Registration page
- `login-reset-password.ftl` - Password reset
- `login-update-password.ftl` - Update password
- `error.ftl` - Error page
- `info.ftl` - Info page
- `terms.ftl` - Terms and conditions

Copy from Keycloak base theme to customize:
```bash
# Find default templates
kubectl exec -it keycloak-0 -n keycloak -- ls /opt/bitnami/keycloak/themes/keycloak/login/
```

## 🧪 Testing

### Local Development

You can test the theme locally using Keycloak in development mode:

```bash
# Start local Keycloak with theme
docker run -p 8080:8080 \
  -e KC_BOOTSTRAP_ADMIN_USERNAME=admin \
  -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
  -v $(pwd)/kube-dc:/opt/keycloak/themes/kube-dc \
  quay.io/keycloak/keycloak:26.0.7 \
  start-dev
```

Visit: http://localhost:8080

### Theme Validation

Check theme is loaded:

```bash
# Check ConfigMap exists
kubectl get configmap kube-dc-theme -n keycloak

# Check volume mount in Keycloak pod
kubectl describe pod keycloak-0 -n keycloak | grep -A 5 "kube-dc-theme"

# Check theme file exists in pod
kubectl exec -it keycloak-0 -n keycloak -- ls -lh /opt/bitnami/keycloak/providers/kube-dc-theme.jar
```

## 📚 Additional Resources

### Keycloak Theme Documentation
- [Server Developer Guide](https://www.keycloak.org/docs/latest/server_development/#_themes)
- [Theme Properties](https://www.keycloak.org/docs/latest/server_development/#theme-properties)
- [Freemarker Templates](https://freemarker.apache.org/docs/)

### PatternFly Resources
- [PatternFly Design Guidelines](https://www.patternfly.org/)
- [Color System](https://www.patternfly.org/v4/design-guidelines/styles/colors)
- [Typography](https://www.patternfly.org/v4/design-guidelines/styles/typography)

## 🐛 Troubleshooting

### Theme Not Appearing in Keycloak Admin

**Problem**: `kube-dc` theme not listed in Themes dropdown

**Solutions**:
1. Verify JAR file exists: `kubectl exec -it keycloak-0 -n keycloak -- ls -l /opt/bitnami/keycloak/providers/`
2. Check file permissions: JAR should be readable by Keycloak user
3. Restart Keycloak: `kubectl rollout restart statefulset/keycloak -n keycloak`
4. Check logs: `kubectl logs keycloak-0 -n keycloak | grep -i theme`

### CSS Not Applied

**Problem**: Theme selected but styles not showing

**Solutions**:
1. Clear browser cache (Ctrl+Shift+R or Cmd+Shift+R)
2. Verify CSS file path in `theme.properties`: `styles=css/kube-dc.css`
3. Check CSS file exists in JAR: `jar tf build/kube-dc-theme.jar | grep css`
4. Inspect page source for CSS loading errors

### Logo Not Displaying

**Problem**: Logo image not showing in header

**Solutions**:
1. Verify logo exists in JAR: `jar tf build/kube-dc-theme.jar | grep logo.png`
2. Check image path in `login.ftl`: `${url.resourcesPath}/img/logo.png`
3. Verify image format is supported (PNG, JPG, SVG)
4. Try with absolute path for debugging

### Volume Mount Issues

**Problem**: ConfigMap not mounting to pod

**Solutions**:
1. Verify ConfigMap exists: `kubectl get cm kube-dc-theme -n keycloak`
2. Check Helm values applied: `helm get values keycloak -n keycloak`
3. Describe pod for mount errors: `kubectl describe pod keycloak-0 -n keycloak`
4. Check for typos in volume name and mountPath

## 🔄 Update Process

To update the theme after making changes:

```bash
# 1. Rebuild theme
./build-theme.sh

# 2. Update ConfigMap
cd build
kubectl apply -f keycloak-theme-configmap.yaml

# 3. Restart Keycloak
kubectl rollout restart statefulset/keycloak -n keycloak

# 4. Wait for pods
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=keycloak -n keycloak --timeout=300s

# 5. Clear browser cache and test
```

## 📄 License

This theme is part of the Kube-DC project and follows the same license.

---

**Built with ❤️ for Kube-DC**
