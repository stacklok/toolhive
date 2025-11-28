#!/usr/bin/env bash

# Script to wrap CRDs with Helm template conditionals
# Based on cert-manager's approach for conditional CRD installation

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Find project root by looking for go.mod file
PROJECT_ROOT="${SCRIPT_DIR}"
while [[ "$PROJECT_ROOT" != "/" && ! -f "$PROJECT_ROOT/go.mod" ]]; do
    PROJECT_ROOT="$(dirname "$PROJECT_ROOT")"
done
if [[ ! -f "$PROJECT_ROOT/go.mod" ]]; then
    echo "Error: Could not find project root (no go.mod found)"
    exit 1
fi
SOURCE_CRD_DIR="${PROJECT_ROOT}/deploy/charts/operator-crds/files/crds"
TARGET_CRD_DIR="${PROJECT_ROOT}/deploy/charts/operator-crds/templates"

# Ensure target directory exists
mkdir -p "${TARGET_CRD_DIR}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}Starting CRD wrapping process...${NC}"
echo "Source directory: ${SOURCE_CRD_DIR}"
echo "Target directory: ${TARGET_CRD_DIR}"

# Function to wrap a single CRD file
wrap_crd() {
    local source_file="$1"
    local filename="$(basename "$source_file")"
    local target_file="${TARGET_CRD_DIR}/${filename}"

    echo -e "${YELLOW}Processing: ${filename}${NC}"

    # Extract the CRD name from the file (e.g., mcpservers.toolhive.stacklok.dev)
    local crd_name
    crd_name=$(grep -m1 "^  name:" "${source_file}" | awk '{print $2}')
    if [ -z "$crd_name" ]; then
        echo -e "${RED}Warning: Could not extract CRD name from ${filename}${NC}"
        crd_name="unknown"
    fi

    echo -e "  CRD name: ${crd_name}"

    # Create the wrapped CRD file
    {
        # Add header from template with filename and CRD name substitutions
        sed -e "s/__CRD_FILENAME__/${filename}/" \
            -e "s/__CRD_NAME__/${crd_name}/" \
            "${SCRIPT_DIR}/crd-header.tpl"
        echo  # Add a newline after the header

        # Add the actual CRD content (skip the first line if it's just '---')
        # and inject the keep annotation after the "annotations:" line
        local content
        if [ "$(head -1 "${source_file}")" = "---" ]; then
            content=$(tail -n +2 "${source_file}")
        else
            content=$(cat "${source_file}")
        fi

        # Inject the keep annotation after "metadata:\n  annotations:"
        echo "$content" | awk '
            /^  annotations:$/ {
                print
                print "    {{- if .Values.crds.keep }}"
                print "    helm.sh/resource-policy: keep"
                print "    {{- end }}"
                next
            }
            { print }
        '

        # Add footer from template
        cat "${SCRIPT_DIR}/crd-footer.tpl"
    } > "${target_file}"

    echo -e "${GREEN}✓ Created: ${target_file}${NC}"
}

# Function to create individual CRD files
create_individual_crds() {
    echo -e "${YELLOW}Creating individual CRD files...${NC}"

    for crd_file in "${SOURCE_CRD_DIR}"/*.yaml; do
        if [ -f "$crd_file" ]; then
            wrap_crd "$crd_file"
        fi
    done
}

# Main execution
main() {
    # Check if source directory exists and has CRD files
    if [ ! -d "${SOURCE_CRD_DIR}" ]; then
        echo -e "${RED}Error: Source CRD directory does not exist: ${SOURCE_CRD_DIR}${NC}"
        exit 1
    fi

    if ! ls "${SOURCE_CRD_DIR}"/*.yaml &>/dev/null; then
        echo -e "${RED}Error: No YAML files found in ${SOURCE_CRD_DIR}${NC}"
        exit 1
    fi

    # Count CRD files
    crd_count=$(ls -1 "${SOURCE_CRD_DIR}"/*.yaml 2>/dev/null | wc -l)
    echo -e "${GREEN}Found ${crd_count} CRD files to process${NC}"

    # Option 1: Create individual wrapped CRD files (recommended for granular control)
    create_individual_crds

    echo -e "${GREEN}✅ CRD wrapping completed successfully!${NC}"
}

# Run the script
main "$@"