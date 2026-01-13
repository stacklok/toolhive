#!/bin/bash
#
# RBAC Test Script for AWS MCP Server via ToolHive
#
# Tests that:
# - Alice (s3-readers group) can list S3 buckets but NOT EC2 instances
# - Bob (ec2-viewers group) can list EC2 instances but NOT S3 buckets
#
# Prerequisites:
# 1. Run the AWS IAM setup: /Users/jakub/devel/manifests/aws-mcp-rbac/setup_rbac_roles.sh
# 2. Create Okta users alice/bob in their respective groups
# 3. Start ToolHive: thv run --from-config aws-mcp-rbac.json
# 4. Get token using oauth2c (interactive authorization_code flow)
#
# Usage:
#   # First get token for the user (opens browser for Okta login):
#   TOKEN=$(oauth2c https://integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697 \
#     --client-id 0oawdyrp42dqFpqbv697 \
#     --client-secret $OKTA_CLIENT_SECRET \
#     --scopes openid,groups,mcp:tools:list,mcp:tools:call \
#     --grant-type authorization_code \
#     --auth-method client_secret_basic 2>/dev/null | jq -r '.access_token')
#
#   # Then run test with the token:
#   ./test_rbac.sh alice "$TOKEN"
#   ./test_rbac.sh bob "$TOKEN"
#

# Don't use set -e as we want to continue even if some commands fail
# set -e

# Configuration
TOOLHIVE_URL="http://127.0.0.1:8080/mcp"

# MCP Protocol version (AWS MCP Server uses 2025-06-18)
MCP_PROTOCOL_VERSION="2025-06-18"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
log_success() { echo -e "${GREEN}[PASS]${NC} $1"; }
log_fail() { echo -e "${RED}[FAIL]${NC} $1"; }

if [ $# -lt 2 ]; then
    echo "Usage: $0 <alice|bob> <token>"
    echo ""
    echo "Arguments:"
    echo "  alice|bob  - User being tested (for expected access validation)"
    echo "  token      - Okta access token obtained via oauth2c"
    echo ""
    echo "Get token using oauth2c (opens browser for Okta login):"
    echo ""
    echo "  TOKEN=\$(oauth2c https://integrator-3683736.okta.com/oauth2/ausw8f1ut6X0WMjZN697 \\"
    echo "    --client-id 0oawdyrp42dqFpqbv697 \\"
    echo "    --client-secret \$OKTA_CLIENT_SECRET \\"
    echo "    --scopes openid,groups,mcp:tools:list,mcp:tools:call \\"
    echo "    --grant-type authorization_code \\"
    echo "    --auth-method client_secret_basic 2>/dev/null | jq -r '.access_token')"
    echo ""
    echo "  ./test_rbac.sh alice \"\$TOKEN\""
    exit 1
fi

USER="$1"
TOKEN="$2"

# Validate user
case "$USER" in
    alice)
        EXPECTED_ROLE="S3ReadOnlyMCPRole"
        ;;
    bob)
        EXPECTED_ROLE="EC2ViewOnlyMCPRole"
        ;;
    *)
        log_error "Unknown user: $USER. Use 'alice' or 'bob'."
        exit 1
        ;;
esac

# Validate token
if [ -z "$TOKEN" ] || [ "$TOKEN" == "null" ]; then
    log_error "Token is empty or invalid"
    exit 1
fi

log_info "Testing as $USER with provided token"

# Decode and show token claims
log_info "Token claims:"
jwt decode "$TOKEN" 2>/dev/null || echo "(could not decode - install jwt-cli)"
echo ""

#------------------------------------------------------------------------------
# Step 2: Initialize MCP session
#------------------------------------------------------------------------------
log_info "Initializing MCP session..."

INIT_HEADERS=$(mktemp)
INIT_RESPONSE=$(curl -s -X POST "$TOOLHIVE_URL" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -D "$INIT_HEADERS" \
    -d "{
        \"jsonrpc\": \"2.0\",
        \"id\": 1,
        \"method\": \"initialize\",
        \"params\": {
            \"protocolVersion\": \"$MCP_PROTOCOL_VERSION\",
            \"capabilities\": {},
            \"clientInfo\": {
                \"name\": \"rbac-test\",
                \"version\": \"1.0.0\"
            }
        }
    }")

echo "Initialize response:"
echo "$INIT_RESPONSE" | jq .
echo ""

# Extract session ID from response header
SESSION_ID=$(grep -i "^Mcp-Session-Id:" "$INIT_HEADERS" | sed 's/^[^:]*: *//' | tr -d '\r\n')
rm -f "$INIT_HEADERS"

if [ -z "$SESSION_ID" ]; then
    log_warn "Could not extract Mcp-Session-Id from response"
else
    log_info "Got session ID: $SESSION_ID"
fi

#------------------------------------------------------------------------------
# Step 3: List available tools
#------------------------------------------------------------------------------
log_info "Listing available tools..."

TOOLS_RESPONSE=$(curl -s -X POST "$TOOLHIVE_URL" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    ${SESSION_ID:+-H "Mcp-Session-Id: $SESSION_ID"} \
    -d '{
        "jsonrpc": "2.0",
        "id": 2,
        "method": "tools/list",
        "params": {}
    }')

echo "Available tools:"
echo "$TOOLS_RESPONSE" | jq '.result.tools[].name' 2>/dev/null || echo "$TOOLS_RESPONSE" | jq .
echo ""

#------------------------------------------------------------------------------
# Step 4: Test S3 access (should work for Alice, fail for Bob)
# AWS MCP uses aws___call_aws tool with command_input parameter
#------------------------------------------------------------------------------
log_info "Testing S3 access (aws s3 ls)..."

S3_RESPONSE=$(curl -s -X POST "$TOOLHIVE_URL" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    ${SESSION_ID:+-H "Mcp-Session-Id: $SESSION_ID"} \
    -d '{
        "jsonrpc": "2.0",
        "id": 3,
        "method": "tools/call",
        "params": {
            "name": "aws___call_aws",
            "arguments": {
                "cli_command": "aws s3 ls"
            }
        }
    }')

echo "S3 ls response:"
echo "$S3_RESPONSE" | jq .
echo ""

# Check if S3 access worked (check for AccessDenied in response)
S3_ERROR=$(echo "$S3_RESPONSE" | jq -r '.result.structuredContent.error // .result.structuredContent.response.error_code // .result.structuredContent.response.error // .error.message // ""' 2>/dev/null)
if echo "$S3_ERROR" | grep -qiE "(AccessDenied|not authorized)"; then
    # S3 access was denied
    if [ "$USER" == "bob" ]; then
        log_success "Bob cannot access S3 (expected - RBAC working!)"
    else
        log_fail "Alice cannot access S3 (NOT expected - check error above)"
    fi
else
    # S3 access worked
    if [ "$USER" == "alice" ]; then
        log_success "Alice can access S3 (expected)"
    else
        log_fail "Bob can access S3 (NOT expected - RBAC may not be working)"
    fi
fi

#------------------------------------------------------------------------------
# Step 5: Test EC2 access (should work for Bob, fail for Alice)
#------------------------------------------------------------------------------
log_info "Testing EC2 access (aws ec2 describe-instances)..."

EC2_RESPONSE=$(curl -s -X POST "$TOOLHIVE_URL" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    ${SESSION_ID:+-H "Mcp-Session-Id: $SESSION_ID"} \
    -d '{
        "jsonrpc": "2.0",
        "id": 4,
        "method": "tools/call",
        "params": {
            "name": "aws___call_aws",
            "arguments": {
                "cli_command": "aws ec2 describe-instances --region us-east-1"
            }
        }
    }')

echo "EC2 describe-instances response:"
echo "$EC2_RESPONSE" | jq .
echo ""

# Check if EC2 access worked (check for UnauthorizedOperation or AccessDenied in response)
EC2_ERROR=$(echo "$EC2_RESPONSE" | jq -r '.result.structuredContent.error // .result.structuredContent.response.error_code // .result.structuredContent.response.error // .error.message // ""' 2>/dev/null)
if echo "$EC2_ERROR" | grep -qiE "(UnauthorizedOperation|AccessDenied|not authorized)"; then
    # EC2 access was denied
    if [ "$USER" == "alice" ]; then
        log_success "Alice cannot access EC2 (expected - RBAC working!)"
    else
        log_fail "Bob cannot access EC2 (NOT expected - check error above)"
    fi
else
    # EC2 access worked
    if [ "$USER" == "bob" ]; then
        log_success "Bob can access EC2 (expected)"
    else
        log_fail "Alice can access EC2 (NOT expected - RBAC may not be working)"
    fi
fi

#------------------------------------------------------------------------------
# Summary
#------------------------------------------------------------------------------
echo ""
log_info "=========================================="
log_info "RBAC Test Summary for $USER"
log_info "=========================================="
echo ""
echo "User: $USER"
echo "Expected IAM Role: $EXPECTED_ROLE"
echo ""
echo "Expected Access:"
if [ "$USER" == "alice" ]; then
    echo "  - S3: ALLOWED"
    echo "  - EC2: DENIED"
else
    echo "  - S3: DENIED"
    echo "  - EC2: ALLOWED"
fi
echo ""
