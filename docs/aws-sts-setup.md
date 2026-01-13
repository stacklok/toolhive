# AWS STS Token Exchange Setup Guide

This guide explains how to configure AWS to accept OIDC tokens from an external identity provider (like Okta) and exchange them for temporary AWS credentials. This enables applications to use their existing identity system to authenticate with AWS services.

## Table of Contents

1. [Overview](#overview)
2. [Prerequisites](#prerequisites)
3. [Key AWS Concepts](#key-aws-concepts)
4. [Step-by-Step Setup](#step-by-step-setup)
5. [Configuration Reference](#configuration-reference)
6. [Trust Policy Deep Dive](#trust-policy-deep-dive)
7. [Permissions for AWS MCP Server](#permissions-for-aws-mcp-server)
8. [Testing the Setup](#testing-the-setup)
9. [Troubleshooting](#troubleshooting)
10. [Security Best Practices](#security-best-practices)

---

## Overview

### The Problem

You have an application that authenticates users via an OIDC provider (like Okta, Auth0, or Keycloak). Now you need those authenticated users to access AWS services, specifically the AWS MCP Server. The challenge is bridging these two identity systems without creating AWS credentials for each user.

### The Solution

AWS Security Token Service (STS) can exchange OIDC tokens for temporary AWS credentials. This is called "Web Identity Federation."

### High-Level Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          Token Exchange Flow                                 │
└─────────────────────────────────────────────────────────────────────────────┘

  ┌──────────┐         ┌──────────────┐         ┌─────────┐         ┌─────────┐
  │  User/   │         │   ToolHive   │         │   AWS   │         │   AWS   │
  │  Client  │         │  Middleware  │         │   STS   │         │   MCP   │
  └────┬─────┘         └──────┬───────┘         └────┬────┘         └────┬────┘
       │                      │                      │                    │
       │  1. Request with     │                      │                    │
       │     OIDC Token       │                      │                    │
       │─────────────────────>│                      │                    │
       │                      │                      │                    │
       │                      │  2. AssumeRoleWith   │                    │
       │                      │     WebIdentity      │                    │
       │                      │─────────────────────>│                    │
       │                      │                      │                    │
       │                      │  3. Temporary AWS    │                    │
       │                      │     Credentials      │                    │
       │                      │<─────────────────────│                    │
       │                      │                      │                    │
       │                      │  4. Request signed   │                    │
       │                      │     with SigV4       │                    │
       │                      │─────────────────────────────────────────>│
       │                      │                      │                    │
       │                      │  5. Response         │                    │
       │                      │<─────────────────────────────────────────│
       │                      │                      │                    │
       │  6. Response         │                      │                    │
       │<─────────────────────│                      │                    │
       │                      │                      │                    │
```

**What happens:**

1. A client sends a request to ToolHive with an OIDC token (obtained from Okta)
2. ToolHive middleware calls AWS STS `AssumeRoleWithWebIdentity` with the OIDC token
3. AWS STS validates the token, checks trust policy, and returns temporary credentials
4. ToolHive signs the outgoing request to AWS MCP Server using AWS SigV4
5. AWS MCP Server validates the signature and processes the request
6. Response flows back to the client

---

## Prerequisites

Before starting, ensure you have:

### Required Access

- **AWS Account** with administrator access (or permissions to create IAM resources)
- **AWS CLI** installed and configured with credentials
  ```bash
  # Verify AWS CLI is installed
  aws --version

  # Verify you have credentials configured
  aws sts get-caller-identity
  ```

### OIDC Provider Information

Gather these details from your identity provider:

| Information | Description | Example |
|-------------|-------------|---------|
| **Issuer URL** | The OIDC provider's issuer identifier | `https://integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697` |
| **Audience** | The `aud` claim value in tokens | `mcpserver` |
| **JWKS Endpoint** | Where AWS fetches signing keys | `<issuer>/.well-known/jwks.json` |

### How to Find OIDC Information

For **Okta**:
1. Go to **Security > API > Authorization Servers**
2. Select your authorization server
3. The **Issuer URI** is shown at the top
4. The **Audience** is configured in your application settings

For **Auth0**:
1. Go to **Applications > APIs**
2. The **Identifier** is your audience
3. The issuer is `https://<your-domain>.auth0.com/`

---

## Key AWS Concepts

If you're new to AWS identity services, here's what you need to know:

### IAM (Identity and Access Management)

Think of IAM as AWS's security system. It controls:
- **Who** can access AWS (authentication)
- **What** they can do (authorization)

IAM has three main components:
- **Users**: Individual identities (not used in this setup)
- **Roles**: Identities that can be assumed temporarily (this is what we'll use)
- **Policies**: Documents that define permissions

### STS (Security Token Service)

STS is AWS's "credential vending machine." Instead of giving out permanent passwords, it issues temporary credentials that automatically expire. This is more secure because:
- Credentials are short-lived (default: 1 hour)
- No permanent secrets to manage or rotate
- Each session can have different permissions

The key STS operation we use is `AssumeRoleWithWebIdentity`:
- **Input**: An OIDC token from your identity provider
- **Output**: Temporary AWS credentials (access key, secret key, session token)

### OIDC Provider in AWS

When you "register an OIDC provider" in AWS, you're telling AWS:
> "I trust this identity system. When someone presents a valid token from this provider, believe who they claim to be."

This creates a trust relationship. AWS will:
1. Fetch the provider's public keys (from the JWKS endpoint)
2. Validate token signatures
3. Check token claims (issuer, audience, expiration)

### IAM Role and Trust Policy

An IAM Role is like a "hat" anyone can wear if they meet certain conditions. The **Trust Policy** defines those conditions.

For OIDC federation, the trust policy says:
> "Allow anyone with a valid token from [OIDC Provider] to assume this role, as long as the token has the correct audience."

### SigV4 (Signature Version 4)

SigV4 is AWS's request signing protocol. Every request to AWS services must be signed to prove:
1. You have valid credentials
2. The request hasn't been tampered with
3. You're making the request now (not a replay attack)

The signing process uses your temporary credentials to add headers to HTTP requests. AWS SDKs handle this automatically.

---

## Step-by-Step Setup

### Step 1: Register the OIDC Provider in AWS

**What we're doing**: Telling AWS about your identity provider so it can validate tokens.

**Why it's needed**: AWS needs to know where to fetch public keys for token validation and what issuer URL to expect.

#### 1.1 Get the OIDC Provider Thumbprint (Optional for Most Providers)

AWS needs a thumbprint of the SSL certificate for providers not hosted on certain trusted domains. For Okta, Auth0, and other major providers, AWS fetches keys securely via HTTPS and the thumbprint may not be strictly required.

If needed, calculate the thumbprint:

```bash
# Replace with your OIDC provider's hostname
OIDC_HOST="integrator-3683736.okta.com"

# Get the certificate chain and extract the root CA thumbprint
openssl s_client -servername $OIDC_HOST -showcerts -connect $OIDC_HOST:443 \
  </dev/null 2>/dev/null | \
  openssl x509 -fingerprint -sha1 -noout | \
  sed 's/://g' | \
  awk -F= '{print tolower($2)}'
```

#### 1.2 Create the OIDC Provider

```bash
# Set your OIDC provider URL (no trailing slash)
OIDC_URL="https://integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697"

# Set the audience value(s) your tokens will have
AUDIENCE="mcpserver"

# Create the OIDC provider
aws iam create-open-id-connect-provider \
  --url "$OIDC_URL" \
  --client-id-list "$AUDIENCE" \
  --thumbprint-list "0000000000000000000000000000000000000000"
```

**Note**: For Okta and other well-known providers, AWS accepts a placeholder thumbprint (`0000000000000000000000000000000000000000`) and validates tokens using the JWKS endpoint directly.

#### 1.3 Verify the Provider Was Created

```bash
# List all OIDC providers
aws iam list-open-id-connect-providers

# Get details about your provider
aws iam get-open-id-connect-provider \
  --open-id-connect-provider-arn "arn:aws:iam::506587498580:oidc-provider/integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697"
```

Expected output:
```json
{
    "Url": "integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697",
    "ClientIDList": ["mcpserver"],
    "ThumbprintList": ["0000000000000000000000000000000000000000"],
    "CreateDate": "2024-01-15T10:30:00Z"
}
```

---

### Step 2: Create the IAM Role with Trust Policy

**What we're doing**: Creating a role that can be assumed by presenting a valid OIDC token.

**Why it's needed**: The role is the "bridge" between the OIDC identity and AWS permissions.

#### 2.1 Create the Trust Policy Document

Create a file called `trust-policy.json`:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::506587498580:oidc-provider/integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697:aud": "mcpserver"
        }
      }
    }
  ]
}
```

**Understanding the trust policy**:

| Field | Purpose |
|-------|---------|
| `Effect: Allow` | Permits the action when conditions are met |
| `Principal.Federated` | The ARN of your registered OIDC provider |
| `Action` | The specific STS operation being permitted |
| `Condition.StringEquals` | Claims that must match in the OIDC token |

#### 2.2 Create the Role

```bash
aws iam create-role \
  --role-name OktaMCPGatewayRole \
  --assume-role-policy-document file://trust-policy.json \
  --description "Role for OIDC-authenticated access to AWS MCP Server"
```

#### 2.3 Verify the Role

```bash
# Check the role exists
aws iam get-role --role-name OktaMCPGatewayRole

# View the trust policy
aws iam get-role --role-name OktaMCPGatewayRole \
  --query 'Role.AssumeRolePolicyDocument' --output json
```

---

### Step 3: Create and Attach Permission Policies

**What we're doing**: Defining what actions the assumed role can perform.

**Why it's needed**: The trust policy only controls who can assume the role. Permission policies control what they can do after assuming it.

#### 3.1 Create the Permission Policy Document

Create a file called `mcp-permissions.json`:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "aws-mcp:InvokeMcp"
      ],
      "Resource": "*"
    }
  ]
}
```

**Note**: Replace `aws-mcp:InvokeMcp` with the actual permissions required by AWS MCP Server. Consult AWS documentation for the exact action names.

#### 3.2 Create and Attach the Policy

```bash
# Create the policy
aws iam create-policy \
  --policy-name MCPServerAccessPolicy \
  --policy-document file://mcp-permissions.json \
  --description "Permissions for AWS MCP Server access"

# Attach the policy to the role
aws iam attach-role-policy \
  --role-name OktaMCPGatewayRole \
  --policy-arn "arn:aws:iam::506587498580:policy/MCPServerAccessPolicy"
```

#### 3.3 Verify the Attachment

```bash
aws iam list-attached-role-policies --role-name OktaMCPGatewayRole
```

Expected output:
```json
{
    "AttachedPolicies": [
        {
            "PolicyName": "MCPServerAccessPolicy",
            "PolicyArn": "arn:aws:iam::506587498580:policy/MCPServerAccessPolicy"
        }
    ]
}
```

---

## Configuration Reference

Use these values when configuring the ToolHive middleware:

| Parameter | Value |
|-----------|-------|
| AWS Account ID | `506587498580` |
| AWS Region | `us-east-1` |
| OIDC Provider URL | `https://integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697` |
| OIDC Provider ARN | `arn:aws:iam::506587498580:oidc-provider/integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697` |
| Audience (aud claim) | `mcpserver` |
| Role Name | `OktaMCPGatewayRole` |
| Role ARN | `arn:aws:iam::506587498580:role/OktaMCPGatewayRole` |

---

## Trust Policy Deep Dive

The trust policy is crucial for security. Let's examine each component:

### Full Trust Policy Structure

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::506587498580:oidc-provider/integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697:aud": "mcpserver"
        }
      }
    }
  ]
}
```

### Version

Always use `"2012-10-17"`. This is the current policy language version and enables all modern features.

### Principal

The `Principal` identifies who is allowed to assume the role:

```json
"Principal": {
  "Federated": "arn:aws:iam::<account-id>:oidc-provider/<provider-url-without-https>"
}
```

**Important**: The provider URL in the ARN does not include `https://`.

### Action

For OIDC federation, the action is always `sts:AssumeRoleWithWebIdentity`.

### Condition Keys

This is where OIDC token validation happens. The condition key format is:

```
<provider-url-without-https>:<claim-name>
```

#### Common Condition Keys

| Condition Key | Token Claim | Purpose |
|---------------|-------------|---------|
| `<provider>:aud` | `aud` | Validates the intended audience |
| `<provider>:sub` | `sub` | Validates the subject (user/client ID) |
| `<provider>:amr` | `amr` | Validates authentication methods |

#### Adding Subject Restrictions

To restrict which users or clients can assume the role, add a subject condition:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::506587498580:oidc-provider/integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697:aud": "mcpserver"
        },
        "StringLike": {
          "integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697:sub": "service-*"
        }
      }
    }
  ]
}
```

This example allows only subjects starting with `service-` to assume the role.

#### Condition Operators

| Operator | Use Case |
|----------|----------|
| `StringEquals` | Exact match (case-sensitive) |
| `StringLike` | Pattern matching with `*` and `?` wildcards |
| `StringEqualsIgnoreCase` | Case-insensitive exact match |
| `ForAnyValue:StringLike` | Match any value in an array claim |

### Updating the Trust Policy

```bash
# Update an existing role's trust policy
aws iam update-assume-role-policy \
  --role-name OktaMCPGatewayRole \
  --policy-document file://trust-policy.json
```

---

## Permissions for AWS MCP Server

### Required Permissions

The permissions your role needs depend on what the AWS MCP Server provides. A typical configuration might include:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "MCPServerAccess",
      "Effect": "Allow",
      "Action": [
        "aws-mcp:InvokeMcp",
        "aws-mcp:ListTools",
        "aws-mcp:ExecuteTool"
      ],
      "Resource": [
        "arn:aws:aws-mcp:us-east-1:506587498580:server/*"
      ]
    }
  ]
}
```

**Note**: The exact action names (`aws-mcp:*`) are placeholders. Consult AWS documentation for the actual AWS MCP Server permission model.

### Scoping Permissions

Follow the principle of least privilege by restricting:

1. **Actions**: Only the specific operations needed
2. **Resources**: Only the specific MCP servers or tools needed
3. **Conditions**: Additional restrictions like source IP or time of day

Example with scoped resources:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "aws-mcp:InvokeMcp",
      "Resource": [
        "arn:aws:aws-mcp:us-east-1:506587498580:server/production-*"
      ],
      "Condition": {
        "IpAddress": {
          "aws:SourceIp": "10.0.0.0/8"
        }
      }
    }
  ]
}
```

---

## Testing the Setup

### Test with AWS CLI

You can test the token exchange using the AWS CLI if you have a valid OIDC token:

```bash
# Set your token (obtained from Okta)
OIDC_TOKEN="eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9..."

# Exchange the token for AWS credentials
aws sts assume-role-with-web-identity \
  --role-arn "arn:aws:iam::506587498580:role/OktaMCPGatewayRole" \
  --role-session-name "test-session" \
  --web-identity-token "$OIDC_TOKEN"
```

Expected successful response:

```json
{
    "Credentials": {
        "AccessKeyId": "ASIA...",
        "SecretAccessKey": "...",
        "SessionToken": "...",
        "Expiration": "2024-01-15T12:30:00Z"
    },
    "SubjectFromWebIdentityToken": "user@example.com",
    "AssumedRoleUser": {
        "AssumedRoleId": "AROA...:test-session",
        "Arn": "arn:aws:sts::506587498580:assumed-role/OktaMCPGatewayRole/test-session"
    },
    "Provider": "arn:aws:iam::506587498580:oidc-provider/integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697",
    "Audience": "mcpserver"
}
```

### Programmatic Testing (Go)

```go
package main

import (
    "context"
    "fmt"

    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/sts"
)

func main() {
    ctx := context.Background()

    // Load AWS config (region only, no credentials needed)
    cfg, err := config.LoadDefaultConfig(ctx,
        config.WithRegion("us-east-1"),
    )
    if err != nil {
        panic(err)
    }

    client := sts.NewFromConfig(cfg)

    token := "your-oidc-token-here"
    roleArn := "arn:aws:iam::506587498580:role/OktaMCPGatewayRole"
    sessionName := "test-session"

    result, err := client.AssumeRoleWithWebIdentity(ctx, &sts.AssumeRoleWithWebIdentityInput{
        RoleArn:          &roleArn,
        RoleSessionName:  &sessionName,
        WebIdentityToken: &token,
    })
    if err != nil {
        panic(err)
    }

    fmt.Printf("Access Key: %s\n", *result.Credentials.AccessKeyId)
    fmt.Printf("Expiration: %s\n", result.Credentials.Expiration)
}
```

---

## Troubleshooting

### Error: "Invalid identity token"

**Symptoms**:
```
An error occurred (InvalidIdentityToken) when calling the AssumeRoleWithWebIdentity operation
```

**Possible Causes**:

| Cause | Solution |
|-------|----------|
| Token expired | Obtain a fresh token from your OIDC provider |
| Wrong audience | Verify the `aud` claim matches what's registered in AWS |
| Wrong issuer | Verify the `iss` claim matches the registered OIDC provider URL |
| Malformed token | Decode the token (jwt.io) and verify structure |

**Debugging**:
```bash
# Decode token payload (without verification)
echo "$OIDC_TOKEN" | cut -d. -f2 | base64 -d 2>/dev/null | jq .
```

Check:
- `iss` matches your OIDC provider URL exactly
- `aud` is in the registered client ID list
- `exp` is in the future

### Error: "Access denied" / "Not authorized to perform sts:AssumeRoleWithWebIdentity"

**Symptoms**:
```
An error occurred (AccessDenied) when calling the AssumeRoleWithWebIdentity operation
```

**Possible Causes**:

| Cause | Solution |
|-------|----------|
| Trust policy mismatch | Verify the OIDC provider ARN in trust policy is correct |
| Condition not met | Check that token claims match trust policy conditions |
| OIDC provider not registered | Verify the provider exists: `aws iam list-open-id-connect-providers` |

**Debugging**:

1. Check the trust policy:
```bash
aws iam get-role --role-name OktaMCPGatewayRole \
  --query 'Role.AssumeRolePolicyDocument'
```

2. Verify token claims match conditions exactly (case-sensitive)

### Error: "No OpenIDConnect provider found"

**Symptoms**:
```
An error occurred (InvalidIdentityToken) when calling the AssumeRoleWithWebIdentity operation:
No OpenIDConnect provider found in your account for https://...
```

**Solution**:

Register the OIDC provider (Step 1 of this guide). Verify with:
```bash
aws iam list-open-id-connect-providers
```

### Error: "Couldn't retrieve verification key"

**Symptoms**:
```
An error occurred (InvalidIdentityToken) when calling the AssumeRoleWithWebIdentity operation:
Couldn't retrieve verification key from your identity provider
```

**Possible Causes**:

| Cause | Solution |
|-------|----------|
| JWKS endpoint unreachable | Verify the `/.well-known/jwks.json` endpoint is accessible |
| SSL/TLS issues | Check certificate chain on OIDC provider |
| Network issues | AWS must be able to reach your OIDC provider publicly |

### Error: Token signature verification failed

**Possible Causes**:

| Cause | Solution |
|-------|----------|
| Key rotation | OIDC provider rotated keys; AWS will fetch new keys automatically |
| Token from wrong provider | Verify token is from the correct authorization server |
| Clock skew | Check system time on token issuer |

---

## Security Best Practices

### 1. Principle of Least Privilege

Only grant the minimum permissions required:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "aws-mcp:InvokeMcp",
      "Resource": "arn:aws:aws-mcp:us-east-1:506587498580:server/specific-server"
    }
  ]
}
```

**Avoid**: `"Resource": "*"` unless absolutely necessary.

### 2. Restrict by Subject Claim

Always add subject restrictions in production to limit which identities can assume the role:

```json
{
  "Condition": {
    "StringEquals": {
      "<provider>:aud": "mcpserver",
      "<provider>:sub": "0oa1234567890abcdef"
    }
  }
}
```

For multiple allowed subjects:
```json
{
  "Condition": {
    "StringEquals": {
      "<provider>:aud": "mcpserver"
    },
    "ForAnyValue:StringEquals": {
      "<provider>:sub": [
        "client-id-1",
        "client-id-2"
      ]
    }
  }
}
```

### 3. Session Duration

Default session duration is 1 hour (3600 seconds). Reduce it for sensitive operations:

```bash
aws sts assume-role-with-web-identity \
  --role-arn "arn:aws:iam::506587498580:role/OktaMCPGatewayRole" \
  --role-session-name "short-session" \
  --web-identity-token "$TOKEN" \
  --duration-seconds 900  # 15 minutes
```

Configure maximum session duration on the role:
```bash
aws iam update-role \
  --role-name OktaMCPGatewayRole \
  --max-session-duration 3600  # 1 hour maximum
```

### 4. Enable CloudTrail Logging

Ensure CloudTrail is logging STS events for audit purposes:

```bash
# Check CloudTrail status
aws cloudtrail describe-trails

# Look for AssumeRoleWithWebIdentity events
aws cloudtrail lookup-events \
  --lookup-attributes AttributeKey=EventName,AttributeValue=AssumeRoleWithWebIdentity \
  --max-results 10
```

Key fields to monitor:
- `userIdentity.webIdFederationData.federatedProvider` - Which OIDC provider was used
- `userIdentity.webIdFederationData.attributes` - Token claims
- `sourceIPAddress` - Where the request came from
- `requestParameters.roleArn` - Which role was assumed

### 5. Use Session Tags (Advanced)

Pass contextual information from OIDC claims as session tags for fine-grained access control:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::506587498580:oidc-provider/integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697:aud": "mcpserver"
        },
        "StringLike": {
          "aws:RequestTag/department": "*"
        }
      }
    },
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::506587498580:oidc-provider/integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697"
      },
      "Action": "sts:TagSession",
      "Condition": {
        "StringEquals": {
          "integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697:aud": "mcpserver"
        }
      }
    }
  ]
}
```

### 6. Rotate and Monitor

- Regularly review trust policies
- Monitor for unusual patterns in CloudTrail
- Remove unused OIDC providers and roles
- Keep your OIDC provider's signing keys secure

---

## Quick Reference

### AWS CLI Commands Summary

```bash
# List OIDC providers
aws iam list-open-id-connect-providers

# Create OIDC provider
aws iam create-open-id-connect-provider \
  --url "https://your-provider.com" \
  --client-id-list "your-audience" \
  --thumbprint-list "0000000000000000000000000000000000000000"

# Create role with trust policy
aws iam create-role \
  --role-name YourRoleName \
  --assume-role-policy-document file://trust-policy.json

# Attach permissions policy
aws iam attach-role-policy \
  --role-name YourRoleName \
  --policy-arn "arn:aws:iam::account-id:policy/PolicyName"

# Test token exchange
aws sts assume-role-with-web-identity \
  --role-arn "arn:aws:iam::account-id:role/RoleName" \
  --role-session-name "test" \
  --web-identity-token "$TOKEN"

# View CloudTrail events
aws cloudtrail lookup-events \
  --lookup-attributes AttributeKey=EventName,AttributeValue=AssumeRoleWithWebIdentity
```

### Configuration Checklist

- [ ] OIDC provider registered in AWS (`aws iam list-open-id-connect-providers`)
- [ ] IAM role created with correct trust policy
- [ ] Trust policy has correct OIDC provider ARN
- [ ] Trust policy has audience condition matching your tokens
- [ ] Permission policy attached to role
- [ ] Permission policy grants required AWS MCP Server actions
- [ ] CloudTrail logging enabled for auditing
- [ ] Subject restrictions added for production use

---

## Additional Resources

- [AWS IAM Documentation](https://docs.aws.amazon.com/IAM/latest/UserGuide/)
- [AWS STS AssumeRoleWithWebIdentity API Reference](https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRoleWithWebIdentity.html)
- [Creating OpenID Connect (OIDC) Identity Providers](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_providers_create_oidc.html)
- [IAM JSON Policy Reference](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies.html)
- [AWS Signature Version 4](https://docs.aws.amazon.com/general/latest/gr/signature-version-4.html)
