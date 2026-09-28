variable "state_passphrase" {
  description = "Passphrase for OpenTofu state encryption (at least 16 characters)."
  type        = string
  sensitive   = true
}

variable "endpoint" {
  description = "Router URL."
  type        = string
  default     = "https://192.168.1.1"
}

variable "username" {
  description = "rpcd login used by the provider (see router/terraform-acl.json)."
  type        = string
  default     = "root"
}

variable "wifi_key" {
  description = "WPA2 passphrase for the wireless network."
  type        = string
  sensitive   = true
  ephemeral   = true
}
