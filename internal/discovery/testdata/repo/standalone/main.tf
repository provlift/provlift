terraform {
  required_providers {
    # Pre-0.13 shorthand: no source, so the address is implied as hashicorp/random.
    random = "~> 3.0"

    # No version argument at all: Terraform will accept any version.
    mongodbatlas = {
      source = "mongodb/mongodbatlas"
    }
  }
}
