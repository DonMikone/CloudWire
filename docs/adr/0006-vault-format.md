# Vault format: wrapped rclone crypt secrets

Each Vault stores random rclone crypt secrets in `vault.json` next to its data, encrypted with keys derived from the vault password and from a Recovery Key (scrypt N=65536,r=8,p=1 + XChaCha20-Poly1305). Any Mac with the password can open the Vault, and the password can change without re-encrypting data; an rclone-compatible export exists for emergencies.
