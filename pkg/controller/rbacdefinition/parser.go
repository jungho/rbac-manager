package rbacdefinition

import (
	"errors"
	"fmt"

	rbacmanagerv1beta1 "github.com/reactiveops/rbac-manager/pkg/apis/rbacmanager/v1beta1"
	logrus "github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// Parser parses RBAC Definitions and determines the Kubernetes resources that it specifies
type Parser struct {
	Clientset                 kubernetes.Interface
	ownerRefs                 []metav1.OwnerReference
	parsedClusterRoleBindings []rbacv1.ClusterRoleBinding
	parsedRoleBindings        []rbacv1.RoleBinding
	parsedServiceAccounts     []v1.ServiceAccount
}

// Parse determines the desired Kubernetes resources an RBAC Definition refers to
func (p *Parser) Parse(rbacDef rbacmanagerv1beta1.RBACDefinition) error {
	if rbacDef.RBACBindings == nil {
		logrus.Warn("No RBACBindings defined")
		return nil
	}

	for _, rbacBinding := range rbacDef.RBACBindings {
		namePrefix := rdNamePrefix(&rbacDef, &rbacBinding)

		err := p.parseRBACBinding(rbacBinding, namePrefix)
		if err != nil {
			return err
		}
	}

	return nil
}

func (p *Parser) parseRBACBinding(rbacBinding rbacmanagerv1beta1.RBACBinding, namePrefix string) error {
	if len(rbacBinding.Subjects) < 1 {
		return errors.New("No subjects specified for RBAC Binding: " + namePrefix)
	}

	for _, requestedSubject := range rbacBinding.Subjects {
		if requestedSubject.Kind == "ServiceAccount" {
			p.parsedServiceAccounts = append(p.parsedServiceAccounts, v1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:            requestedSubject.Name,
					Namespace:       requestedSubject.Namespace,
					OwnerReferences: p.ownerRefs,
					Labels:          Labels,
				},
			})
		}
	}

	if rbacBinding.ClusterRoleBindings != nil {
		for _, requestedCRB := range rbacBinding.ClusterRoleBindings {
			err := p.parseClusterRoleBinding(requestedCRB, rbacBinding.Subjects, namePrefix)
			if err != nil {
				return err
			}
		}
	}

	if rbacBinding.RoleBindings != nil {
		for _, requestedRB := range rbacBinding.RoleBindings {
			err := p.parseRoleBinding(requestedRB, rbacBinding.Subjects, namePrefix)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Parser) parseClusterRoleBinding(
	crb rbacmanagerv1beta1.ClusterRoleBinding, subjects []rbacv1.Subject, prefix string) error {
	crbName := fmt.Sprintf("%v-%v", prefix, crb.ClusterRole)

	p.parsedClusterRoleBindings = append(p.parsedClusterRoleBindings, rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:            crbName,
			OwnerReferences: p.ownerRefs,
			Labels:          Labels,
		},
		RoleRef: rbacv1.RoleRef{
			Kind: "ClusterRole",
			Name: crb.ClusterRole,
		},
		Subjects: subjects,
	})

	return nil
}

func (p *Parser) parseRoleBinding(
	rb rbacmanagerv1beta1.RoleBinding, subjects []rbacv1.Subject, prefix string) error {

	objectMeta := metav1.ObjectMeta{
		OwnerReferences: p.ownerRefs,
		Labels:          Labels,
	}

	var requestedRoleName string
	var roleRef rbacv1.RoleRef

	if rb.ClusterRole != "" {
		logrus.Debugf("Processing Requested ClusterRole %v <> %v <> %v", rb.ClusterRole, rb.Namespace, rb)
		requestedRoleName = rb.ClusterRole
		roleRef = rbacv1.RoleRef{
			Kind: "ClusterRole",
			Name: rb.ClusterRole,
		}
	} else if rb.Role != "" {
		logrus.Debugf("Processing Requested Role %v <> %v <> %v", rb.Role, rb.Namespace, rb)
		requestedRoleName = fmt.Sprintf("%v-%v", rb.Role, rb.Namespace)
		roleRef = rbacv1.RoleRef{
			Kind: "Role",
			Name: rb.Role,
		}
	} else {
		return errors.New("Invalid role binding, role or clusterRole required")
	}

	objectMeta.Name = fmt.Sprintf("%v-%v", prefix, requestedRoleName)

	if rb.NamespaceSelector.MatchLabels != nil {
		logrus.Debugf("Processing Namespace Selector %v", rb.NamespaceSelector)

		listOptions := metav1.ListOptions{LabelSelector: labels.Set(rb.NamespaceSelector.MatchLabels).String()}
		namespaces, err := p.Clientset.CoreV1().Namespaces().List(listOptions)
		if err != nil {
			return err
		}

		for _, namespace := range namespaces.Items {
			logrus.Debugf("Adding Role Binding With Dynamic Namespace %v", namespace.Name)

			om := objectMeta
			om.Namespace = namespace.Name

			p.parsedRoleBindings = append(p.parsedRoleBindings, rbacv1.RoleBinding{
				ObjectMeta: om,
				RoleRef:    roleRef,
				Subjects:   subjects,
			})
		}

	} else if rb.Namespace != "" {
		objectMeta.Namespace = rb.Namespace

		p.parsedRoleBindings = append(p.parsedRoleBindings, rbacv1.RoleBinding{
			ObjectMeta: objectMeta,
			RoleRef:    roleRef,
			Subjects:   subjects,
		})

	} else {
		return errors.New("Invalid role binding, namespace or namespace selector required")
	}

	return nil
}

func (p *Parser) hasNamespaceSelectors(rbacDef *rbacmanagerv1beta1.RBACDefinition) bool {
	for _, rbacBinding := range rbacDef.RBACBindings {
		for _, roleBinding := range rbacBinding.RoleBindings {
			if roleBinding.Namespace == "" && roleBinding.NamespaceSelector.MatchLabels != nil {
				return true
			}
		}
	}
	return false
}

func (p *Parser) parseRoleBindings(rbacDef *rbacmanagerv1beta1.RBACDefinition) {
	for _, rbacBinding := range rbacDef.RBACBindings {
		for _, roleBinding := range rbacBinding.RoleBindings {
			namePrefix := rdNamePrefix(rbacDef, &rbacBinding)
			p.parseRoleBinding(roleBinding, rbacBinding.Subjects, namePrefix)
		}
	}
}

func rdNamePrefix(rbacDef *rbacmanagerv1beta1.RBACDefinition, rbacBinding *rbacmanagerv1beta1.RBACBinding) string {
	return fmt.Sprintf("%v-%v", rbacDef.Name, rbacBinding.Name)
}
