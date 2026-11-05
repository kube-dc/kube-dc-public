import React from 'react';

import ApiIcon from '../../../../docs/diagrams/icons/api.svg';
import ApplicationIcon from '../../../../docs/diagrams/icons/application-web.svg';
import DataIcon from '../../../../docs/diagrams/icons/data-service.svg';
import KeycloakIcon from '../../../../docs/diagrams/icons/keycloak.svg';
import KubernetesIcon from '../../../../docs/diagrams/icons/kubernetes.svg';
import SecurityIcon from '../../../../docs/diagrams/icons/security.svg';
import {
  DiagramCallout,
  DiagramEdge,
  DiagramNode,
  DiagramSectionLabel,
  ExplainerDiagram,
} from './index';

export function HostedMcpFlowDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The AI app signs in on the Organization realm and calls the MCP endpoint. The endpoint exchanges the app's token for a token of the same user, so Kubernetes RBAC and the audit log see the person."
      description="An AI app such as Claude, VS Code, Cursor, or ChatGPT signs in on the Organization's Keycloak realm and receives a token with the endpoint's URL in its audience and the kube-dc:mcp scope. It calls the hosted MCP endpoint at /mcp/<organization>. The endpoint, the kube-dc-backend-mcp Deployment, has no Kubernetes rights of its own. It asks the realm's confidential client kube-dc-mcp to exchange the app's token for a token of the same user, with OAuth token exchange (RFC 8693). With that token it calls the Kubernetes API, under the user's own RBAC, and the kube-dc backend, for VM logic, price quotes, and managed services. It writes every tool call as an audit event to the audit store, with service mcp and the tool name as the action."
      diagramId="hosted-mcp-flow"
      minWidth={900}
      title="Hosted MCP sign-in and tool call"
      viewBox="0 0 900 600"
    >
      <DiagramEdge bidirectional d="M270 101 H620" label="sign in, token" labelWidth={140} labelX={445} labelY={106} />
      <DiagramEdge d="M150 142 C150 186 390 186 390 218 V230" label="tool call" labelWidth={96} labelX={250} labelY={190} />
      <DiagramEdge bidirectional d="M510 230 V218 C510 186 745 186 745 154 V142" kind="control" label="token exchange" labelWidth={140} labelX={640} labelY={190} />
      <DiagramEdge d="M450 312 C450 356 155 356 155 388 V400" kind="control" />
      <DiagramEdge d="M450 312 V400" kind="control" />
      <DiagramEdge d="M450 312 C450 356 745 356 745 388 V400" kind="data" />
      <DiagramSectionLabel label="SIGN-IN · EXCHANGE · CALL · AUDIT" lineTo={872} x={28} y={28} />
      <DiagramNode detail={['Claude, VS Code,', 'Cursor, ChatGPT']} height={82} icon={ApplicationIcon} title="AI app" tone="external" width={240} x={30} y={60} />
      <DiagramNode detail={['audience + scope', 'client kube-dc-mcp']} height={82} icon={KeycloakIcon} title="Organization realm" width={250} x={620} y={60} />
      <DiagramNode detail={['/mcp/<organization>', 'no RBAC of its own']} height={82} icon={ApiIcon} title="MCP endpoint" tone="accent" width={240} x={330} y={230} />
      <DiagramNode detail={["the user's own RBAC", 'audit names person']} height={82} icon={KubernetesIcon} title="Kubernetes API" width={250} x={30} y={400} />
      <DiagramNode detail={['VM logic, quotes,', 'managed services']} height={82} title="kube-dc backend" width={250} x={325} y={400} />
      <DiagramNode detail={['service mcp', 'action = tool name']} height={82} icon={DataIcon} title="Audit store" tone="storage" width={250} x={620} y={400} />
      <DiagramCallout detail="Keycloak exchanges it for a token of the same user before any Kubernetes call." height={64} title="The app's token never reaches Kubernetes" width={840} x={30} y={510} />
    </ExplainerDiagram>
  );
}

export function McpAppRegistrationDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="All three registration paths end in the same Organization realm, under limits that the manager sets. No registered app can add the claim that makes a token a Kubernetes credential."
      description="An AI app registers in an Organization realm in one of three ways. With a Client ID Metadata Document, the metadata URL must be on a host in backend.mcp.cimdClientDomains. Pre-registered apps, such as ChatGPT and Codex, come from backend.mcp.preregisteredApps and are created in every realm. With Dynamic Client Registration, every redirect URI must be on a host in backend.mcp.trustedRedirectHosts. The manager limits each realm to at most 200 registered apps, with no claim mappers and only the default scopes plus kube-dc:mcp and kube-dc:mcp:read. Only the platform's own client adds the org claim that makes a token a Kubernetes credential."
      diagramId="hosted-mcp-registration"
      minWidth={900}
      title="How AI apps register in an Organization realm"
      viewBox="0 0 900 440"
    >
      <DiagramEdge d="M165 142 C165 186 360 186 360 218 V230" kind="control" />
      <DiagramEdge d="M450 142 V230" kind="control" />
      <DiagramEdge d="M735 142 C735 186 540 186 540 218 V230" kind="control" />
      <DiagramSectionLabel label="THREE WAYS TO REGISTER" lineTo={872} x={28} y={28} />
      <DiagramNode detail={['metadata URL host', 'in cimdClientDomains']} height={82} title="Client ID metadata" width={270} x={30} y={60} />
      <DiagramNode detail={['preregisteredApps', 'ChatGPT, Codex']} height={82} title="Pre-registered" width={270} x={315} y={60} />
      <DiagramNode detail={['redirect host in', 'trustedRedirectHosts']} height={82} title="Dynamic registration" width={270} x={600} y={60} />
      <DiagramNode detail={['at most 200 apps', 'no claim mappers']} height={82} icon={KeycloakIcon} title="Organization realm" tone="accent" width={300} x={300} y={230} />
      <DiagramCallout detail="A registered app's token is never a Kubernetes credential on its own." height={64} title="Only the platform's client adds the org claim" width={840} x={30} y={350} />
    </ExplainerDiagram>
  );
}

export function McpReadOnlyDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="With enforceReadOnly, a read-only session is read-only twice: the MCP server offers only read tools, and Kubernetes rejects every change made with the session's token."
      description="An AI app connects to a URL that ends in /read_only and asks only for the kube-dc:mcp:read scope. The MCP server offers only the tools marked read-only. With backend.mcp.enforceReadOnly, it exchanges the token for audience kube-dc-mcp-read with scope kube-dc:mcp-session-read, which adds the claim kube_dc_access=read. A second OpenIDConnect per realm, mcp-read.<organization>, accepts only that audience and copies the claim to the user's extra, gardener.cloud/user/kube_dc_access. The ValidatingAdmissionPolicy kube-dc-mcp-read-only denies CREATE, UPDATE, DELETE, and CONNECT requests that carry that extra, so the Kubernetes API serves only reads. Without the read token, the server refuses the session and never falls back to a full token."
      diagramId="hosted-mcp-read-only"
      minWidth={900}
      title="Kubernetes-enforced read-only sessions"
      viewBox="0 0 900 440"
    >
      <DiagramEdge d="M280 101 H325" />
      <DiagramEdge d="M575 101 H620" kind="control" />
      <DiagramEdge d="M745 142 V230" kind="control" />
      <DiagramEdge d="M620 271 H575" kind="control" />
      <DiagramEdge d="M325 271 H280" kind="control" />
      <DiagramSectionLabel label="READ-ONLY IN THE SERVER AND IN KUBERNETES" lineTo={872} x={28} y={28} />
      <DiagramNode detail={['/mcp/<org>/read_only', 'kube-dc:mcp:read']} height={82} title="Read-only URL" tone="external" width={250} x={30} y={60} />
      <DiagramNode detail={['read tools only', 'fails closed']} height={82} icon={ApiIcon} title="MCP server" width={250} x={325} y={60} />
      <DiagramNode detail={['aud kube-dc-mcp-read', 'kube_dc_access=read']} height={82} icon={KeycloakIcon} title="Token exchange" width={250} x={620} y={60} />
      <DiagramNode detail={['OpenIDConnect', 'claim to user extra']} height={82} title="mcp-read.<org>" width={250} x={620} y={230} />
      <DiagramNode detail={['kube-dc-mcp-read-only', 'denies writes, exec']} height={82} icon={SecurityIcon} title="Admission policy" tone="accent" width={250} x={325} y={230} />
      <DiagramNode detail={['reads succeed', 'changes rejected']} height={82} icon={KubernetesIcon} title="Kubernetes API" width={250} x={30} y={230} />
      <DiagramCallout detail="Without the read token, the server refuses the session. It never falls back to a full token." height={64} title="Fails closed" tone="warning" width={840} x={30} y={350} />
    </ExplainerDiagram>
  );
}
