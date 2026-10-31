terraform {
  required_version = ">= 1.5.0"

  required_providers {
    # Alignment and comments here must survive an upgrade untouched.
    github = {
      source  = "integrations/github"
      version = "~> 6.0"
    }

    azurerm = {
      source  = "hashicorp/azurerm"
      version = ">= 3.0, < 4.0"
    }
  }
}

provider "azurerm" {
  features {}
}

module "network" {
  source = "./modules/network"
  name   = "test-network"
}
