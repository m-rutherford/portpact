import { SSMClient, StartSessionCommand } from '@aws-sdk/client-ssm';

const ssm = new SSMClient({});

interface LambdaEvent {
  headers?: Record<string, string | undefined>;
  body?: string | null;
}

interface LambdaResponse {
  statusCode: number;
  headers: Record<string, string>;
  body: string;
}

function json(statusCode: number, body: unknown): LambdaResponse {
  return {
    statusCode,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  };
}

function mustEnv(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`Missing env var: ${name}`);
  return v;
}

function getHeader(headers: Record<string, string | undefined>, name: string): string | undefined {
  // Case-insensitive header lookup
  const lower = name.toLowerCase();
  for (const [key, value] of Object.entries(headers)) {
    if (key.toLowerCase() === lower) return value;
  }
  return undefined;
}

export const handler = async (event: LambdaEvent): Promise<LambdaResponse> => {
  try {
    // Authenticate via API key
    const apiKeyExpected = mustEnv('BROKER_API_KEY');
    const headers = event.headers ?? {};
    const apiKeyGot = getHeader(headers, 'x-api-key') ?? '';

    if (apiKeyGot !== apiKeyExpected) {
      return json(401, { error: 'unauthorized' });
    }

    // Load config from environment
    const gatewayInstanceId = mustEnv('GATEWAY_INSTANCE_ID');
    const allowedHost = mustEnv('ALLOWED_RDS_HOST');
    const allowedPort = parseInt(mustEnv('ALLOWED_RDS_PORT'), 10);

    // Parse request body
    const rawBody = event.body ?? '';
    const body = rawBody ? JSON.parse(rawBody) : {};

    // Validate target (only rds-postgres supported for now)
    const target = body.target ?? 'rds-postgres';
    if (target !== 'rds-postgres') {
      return json(400, { error: 'invalid_target', allowed: ['rds-postgres'] });
    }

    // Local port is informational for the client; remote port is fixed
    const localPort = Number.isInteger(body.localPort) ? body.localPort : allowedPort;

    // Start SSM session for port forwarding
    const command = new StartSessionCommand({
      Target: gatewayInstanceId,
      DocumentName: 'AWS-StartPortForwardingSessionToRemoteHost',
      Parameters: {
        host: [allowedHost],
        portNumber: [String(allowedPort)],
        localPortNumber: [String(localPort)],
      },
    });

    const resp = await ssm.send(command);

    return json(200, {
      sessionId: resp.SessionId,
      streamUrl: resp.StreamUrl,
      tokenValue: resp.TokenValue,
      target: {
        host: allowedHost,
        port: allowedPort,
      },
      localPort,
    });
  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : String(err);
    console.error('broker_error', err);
    return json(500, { error: 'internal_error', message });
  }
};
